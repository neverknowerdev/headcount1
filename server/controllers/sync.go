package endpoints

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
)

func (api *API) SyncDBWithFilesystem(ctx context.Context) error {
	settings := LoadSettings()
	log.Printf("Syncing DB with filesystem at %s", settings.BasePath)
	fm := filesystem.NewManager(settings.BasePath)
	if err := fm.SetupBaseDirectories(); err != nil {
		return err
	}

	// Phase 1: Export current DB → filesystem (only writes files that don't exist yet).
	// This handles the case where a worktree has existing DB data from before write-through
	// was added, ensuring that data reaches the filesystem for other worktrees to pick up.
	api.exportDBToFilesystem(fm, settings.BasePath)

	// Phase 2: Import filesystem → DB (create DB records from filesystem files).
	// This handles the case where a fresh-DB worktree needs to pick up data written by
	// another worktree.

	// 1. Sync Companies (using preserved ID from companies/{shortName}/settings.yml)
	compShortNames, err := fm.ListCompanies()
	if err != nil {
		log.Printf("Error listing companies: %v", err)
		return err
	}
	log.Printf("Found %d companies on disk: %v", len(compShortNames), compShortNames)

	for _, shortName := range compShortNames {
		var existing db.Company
		if err := api.db.Where("short_name = ?", shortName).First(&existing).Error; err != nil {
			log.Printf("Creating missing company in DB: %s", shortName)

			newComp := db.Company{
				Name:      shortName,
				ShortName: shortName,
				Color:     "#4F46E5",
			}
			// Preserve company ID and name from settings.yml when available
			if compSettings, err := fm.ReadCompanySettings(shortName); err == nil && compSettings.CompanyID != 0 {
				newComp.ID = compSettings.CompanyID
				if compSettings.Name != "" {
					newComp.Name = compSettings.Name
				}
			}

			if err := api.db.Create(&newComp).Error; err == nil {
				existing = newComp
			} else {
				log.Printf("Failed to create company %s: %v", shortName, err)
				continue
			}
		}

		// 2. Sync Projects for this company
		projNames, err := fm.ListProjects(shortName)
		if err != nil {
			continue
		}
		log.Printf("Found %d projects for %s: %v", len(projNames), shortName, projNames)

		for _, projName := range projNames {
			var existingProj db.Project
			if err := api.db.Where("company_id = ? AND name = ?", existing.ID, projName).First(&existingProj).Error; err != nil {
				log.Printf("Creating missing project in DB: %s/%s", shortName, projName)
				newProj := db.Project{
					CompanyID:       int32(existing.ID),
					Name:            projName,
					WorkspaceFolder: shortName + "/" + projName,
				}
				api.db.Create(&newProj)
			}
		}

		// 3. Sync MCP Servers for this company (with preserved IDs, no auth tokens)
		mcpRecs, mcpErr := fm.ListMCPServersFromDisk(shortName)
		if mcpErr != nil {
			log.Printf("Error listing MCP servers for %s: %v", shortName, mcpErr)
		} else {
			for _, rec := range mcpRecs {
				var existingMCP db.MCPServer
				if api.db.First(&existingMCP, rec.ID).Error != nil {
					s := db.MCPServer{
						ID:          rec.ID,
						CompanyID:   existing.ID,
						Name:        rec.Name,
						DisplayName: rec.DisplayName,
						Description: rec.Description,
						Transport:   rec.Transport,
						Command:     rec.Command,
						Args:        rec.Args,
						URL:         rec.URL,
						Headers:     rec.Headers,
						AuthType:    rec.AuthType,
						Enabled:     rec.Enabled,
						Builtin:     rec.Builtin,
					}
					if err := api.db.Omit("Company").Create(&s).Error; err != nil {
						log.Printf("Failed to restore MCP server %d (%s): %v", rec.ID, rec.Name, err)
					} else {
						log.Printf("Restored MCP server %d (%s) for %s", rec.ID, rec.Name, shortName)
					}
				}
			}
		}

		// 4. Sync Sprints for this company (with preserved IDs)
		sprintRecords, err := fm.ListSprintsFromDisk(shortName)
		if err != nil {
			log.Printf("Error listing sprints for %s: %v", shortName, err)
		} else {
			for _, rec := range sprintRecords {
				var existingSprint db.Sprint
				if api.db.First(&existingSprint, rec.ID).Error != nil {
					sprint := db.Sprint{
						ID:        rec.ID,
						CompanyID: existing.ID,
						Name:      rec.Name,
						Goal:      rec.Goal,
						StartDate: rec.StartDate,
						EndDate:   rec.EndDate,
					}
					if err := api.db.Omit("Company").Create(&sprint).Error; err != nil {
						log.Printf("Failed to create sprint %d (%s): %v", rec.ID, rec.Name, err)
					} else {
						log.Printf("Restored sprint %d (%s) for %s", rec.ID, rec.Name, shortName)
					}
				}
			}
		}
	}

	// 4. Sync LLM Providers (global, no company FK)
	providers, err := fm.ListLLMProvidersFromDisk()
	if err != nil {
		log.Printf("Error listing LLM providers: %v", err)
	} else {
		for _, p := range providers {
			var existing db.LLMProvider
			if api.db.First(&existing, p.ID).Error != nil {
				if err := api.db.Create(&p).Error; err != nil {
					log.Printf("Failed to create LLM provider %d: %v", p.ID, err)
				} else {
					log.Printf("Restored LLM provider %d (%s)", p.ID, p.Name)
				}
			}
		}
	}

	// 5. Sync Tasks (after companies and sprints are in DB)
	for _, shortName := range compShortNames {
		var comp db.Company
		if api.db.Where("short_name = ?", shortName).First(&comp).Error != nil {
			continue
		}

		taskRecords, err := fm.ListTasksFromDisk(shortName)
		if err != nil {
			log.Printf("Error listing tasks for %s: %v", shortName, err)
			continue
		}

		// Sort by ID so parents are created before children
		sort.Slice(taskRecords, func(i, j int) bool {
			return taskRecords[i].ID < taskRecords[j].ID
		})

		for _, rec := range taskRecords {
			var existing db.Task
			if api.db.First(&existing, rec.ID).Error != nil {
				// Verify sprint is present
				var sprint db.Sprint
				if api.db.First(&sprint, rec.SprintID).Error != nil {
					log.Printf("Sprint %d not found for task %d, skipping", rec.SprintID, rec.ID)
					continue
				}

				task := db.Task{
					ID:          rec.ID,
					CompanyID:   comp.ID,
					SprintID:    rec.SprintID,
					Title:       rec.Title,
					TaskType:    rec.TaskType,
					Description: rec.Description,
					Priority:    rec.Priority,
					Status:      rec.Status,
					DueDate:     rec.DueDate,
					IsArchived:  rec.IsArchived,
				}

				if rec.ProjectID != nil {
					var proj db.Project
					if api.db.First(&proj, *rec.ProjectID).Error == nil {
						task.ProjectID = rec.ProjectID
					}
				}
				if rec.AgentID != nil {
					var agent db.Agent
					if api.db.First(&agent, *rec.AgentID).Error == nil {
						task.AgentID = rec.AgentID
					}
				}
				if rec.ParentID != nil {
					var parent db.Task
					if api.db.First(&parent, *rec.ParentID).Error == nil {
						task.ParentID = rec.ParentID
					}
				}

				if err := api.db.Omit("Company", "Project", "Sprint", "Agent", "Parent").Create(&task).Error; err != nil {
					log.Printf("Failed to create task %d: %v", rec.ID, err)
					continue
				}
				log.Printf("Restored task %d (%s) for %s", rec.ID, rec.Title, shortName)
			}

			// Import comments for this task (whether the task was just created or already existed)
			commentRecs, err := fm.ListTaskCommentsFromDisk(shortName, rec.ID)
			if err != nil {
				log.Printf("Error listing comments for task %d: %v", rec.ID, err)
				continue
			}
			for _, cRec := range commentRecs {
				var existingComment db.Comment
				if api.db.First(&existingComment, cRec.ID).Error != nil {
					comment := db.Comment{
						ID:         cRec.ID,
						TaskID:     cRec.TaskID,
						AuthorType: cRec.AuthorType,
						AuthorID:   cRec.AuthorID,
						Content:    cRec.Content,
						CreatedAt:  cRec.CreatedAt,
						UpdatedAt:  cRec.UpdatedAt,
					}
					if err := api.db.Omit("Task").Create(&comment).Error; err != nil {
						log.Printf("Failed to create comment %d for task %d: %v", cRec.ID, rec.ID, err)
					} else {
						log.Printf("Restored comment %d for task %d", cRec.ID, rec.ID)
					}
				}
			}
		}
	}

	return nil
}

// exportDBToFilesystem writes current DB records to disk for any that don't have a file yet.
// This is safe to call repeatedly — it never overwrites existing files.
func (api *API) exportDBToFilesystem(fm *filesystem.Manager, basePath string) {
	// Export companies (settings.yml)
	var companies []db.Company
	api.db.Find(&companies)
	for _, comp := range companies {
		settingsPath := filepath.Join(basePath, "companies", comp.ShortName, "settings.yml")
		if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
			if err := fm.WriteCompanySettings(comp); err != nil {
				log.Printf("Export: failed to write company settings for %s: %v", comp.ShortName, err)
			}
		}
	}

	// Export LLM providers
	var providers []db.LLMProvider
	api.db.Find(&providers)
	for _, p := range providers {
		providerPath := filepath.Join(basePath, "data", "llm-providers", formatID(p.ID)+".json")
		if _, err := os.Stat(providerPath); os.IsNotExist(err) {
			if err := fm.SaveLLMProvider(p); err != nil {
				log.Printf("Export: failed to write provider %d: %v", p.ID, err)
			}
		}
	}

	// Build company ID → shortName map for sprint/task export
	compByID := make(map[int32]db.Company, len(companies))
	for _, c := range companies {
		compByID[c.ID] = c
	}

	// Export sprints
	var sprints []db.Sprint
	api.db.Find(&sprints)
	for _, s := range sprints {
		comp, ok := compByID[s.CompanyID]
		if !ok {
			continue
		}
		sprintPath := filepath.Join(basePath, "companies", comp.ShortName, "sprints", formatID(s.ID)+".json")
		if _, err := os.Stat(sprintPath); os.IsNotExist(err) {
			if err := fm.SaveSprint(comp, s); err != nil {
				log.Printf("Export: failed to write sprint %d: %v", s.ID, err)
			}
		}
	}

	// Export MCP servers (per company)
	var mcpServers []db.MCPServer
	api.db.Find(&mcpServers)
	for _, s := range mcpServers {
		comp, ok := compByID[s.CompanyID]
		if !ok {
			continue
		}
		mcpPath := filepath.Join(basePath, "companies", comp.ShortName, "mcp-servers", formatID(s.ID)+".json")
		if _, err := os.Stat(mcpPath); os.IsNotExist(err) {
			if err := fm.SaveMCPServer(comp, s); err != nil {
				log.Printf("Export: failed to write MCP server %d: %v", s.ID, err)
			}
		}
	}

	// Export tasks and their comments
	var tasks []db.Task
	api.db.Find(&tasks)
	for _, t := range tasks {
		comp, ok := compByID[t.CompanyID]
		if !ok {
			continue
		}
		taskJsonPath := filepath.Join(basePath, "companies", comp.ShortName, "tasks", formatID(t.ID), "task.json")
		if _, err := os.Stat(taskJsonPath); os.IsNotExist(err) {
			if err := fm.SaveTask(comp, t); err != nil {
				log.Printf("Export: failed to write task %d: %v", t.ID, err)
			}
		}
		commentsPath := filepath.Join(basePath, "companies", comp.ShortName, "tasks", formatID(t.ID), "comments.json")
		if _, err := os.Stat(commentsPath); os.IsNotExist(err) {
			var comments []db.Comment
			api.db.Where("task_id = ?", t.ID).Order("created_at asc").Find(&comments)
			if err := fm.SaveTaskComments(comp, t.ID, comments); err != nil {
				log.Printf("Export: failed to write comments for task %d: %v", t.ID, err)
			}
		}
	}
}

func formatID(id int32) string {
	return fmt.Sprintf("%d", id)
}

func (api *API) SyncSettings(w http.ResponseWriter, r *http.Request) {
	err := api.SyncDBWithFilesystem(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
