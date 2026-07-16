package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"

	"gorm.io/gorm"
)

func RestoreBackup(archivePath, basePath string, database *gorm.DB) error {
	log.Printf("Starting restore from %s...", archivePath)

	// Extract archive to a temp directory
	tempDir, err := os.MkdirTemp("", "paperclip-restore-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	if err := extractArchive(archivePath, tempDir); err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}

	// Restore filesystem data
	if err := restoreFilesystem(tempDir, basePath); err != nil {
		log.Printf("Warning: failed to restore filesystem data: %v", err)
	}

	// Rebuild DB from filesystem
	if err := rebuildDBFromFS(basePath, database); err != nil {
		return fmt.Errorf("failed to rebuild database: %w", err)
	}

	log.Printf("Restore completed successfully")
	return nil
}

func extractArchive(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func restoreFilesystem(tempDir, basePath string) error {
	// Restore data directory
	srcData := filepath.Join(tempDir, "data")
	if _, err := os.Stat(srcData); err == nil {
		dstData := filepath.Join(basePath, "data")
		if err := copyDir(srcData, dstData); err != nil {
			return fmt.Errorf("failed to restore data directory: %w", err)
		}
	}

	// Restore workspace directory
	srcWorkspace := filepath.Join(tempDir, "workspace")
	if _, err := os.Stat(srcWorkspace); err == nil {
		dstWorkspace := filepath.Join(basePath, "workspace")
		if err := copyDir(srcWorkspace, dstWorkspace); err != nil {
			return fmt.Errorf("failed to restore workspace directory: %w", err)
		}
	}

	// Restore companies directory (Manager format: settings, sprints, tasks, comments)
	srcCompanies := filepath.Join(tempDir, "companies")
	if _, err := os.Stat(srcCompanies); err == nil {
		dstCompanies := filepath.Join(basePath, "companies")
		if err := copyDir(srcCompanies, dstCompanies); err != nil {
			return fmt.Errorf("failed to restore companies directory: %w", err)
		}
	}

	// Restore settings.yaml
	srcSettings := filepath.Join(tempDir, "settings.yaml")
	if _, err := os.Stat(srcSettings); err == nil {
		dstSettings := filepath.Join(basePath, "settings.yaml")
		data, err := os.ReadFile(srcSettings)
		if err == nil {
			os.WriteFile(dstSettings, data, 0644)
		}
	}

	return nil
}

func rebuildDBFromFS(basePath string, database *gorm.DB) error {
	ctx := database.Statement.Context
	if ctx == nil {
		ctx = database.Statement.Context
	}

	// Wipe existing data
	tables := []string{
		"activity_logs", "proxy_request_logs", "runs", "comments",
		"attachments", "tasks", "skills", "agents", "llm_providers",
		"sprints", "projects", "companies",
	}
	for _, table := range tables {
		database.WithContext(ctx).Exec("DELETE FROM " + table)
	}
	database.WithContext(ctx).Exec("DELETE FROM sqlite_sequence")

	storage := filesystem.NewStorage(basePath)
	fm := filesystem.NewManager(basePath)

	// 1. Restore LLM Providers
	providers, err := storage.ReadProviders()
	if err != nil {
		log.Printf("Warning: failed to read providers: %v", err)
	} else {
		providerIDMap := map[int32]int32{}
		for _, p := range providers {
			oldID := p.ID
			provider := db.LLMProvider{
				Name:            p.Name,
				BaseUrl:         p.BaseUrl,
				ApiKey:          p.ApiKey,
				ProviderType:    p.ProviderType,
				DefaultModel:    p.DefaultModel,
				SupportedModels: p.SupportedModels,
			}
			if err := database.WithContext(ctx).Create(&provider).Error; err != nil {
				log.Printf("Warning: failed to restore provider %s: %v", p.Name, err)
				continue
			}
			providerIDMap[oldID] = provider.ID
		}

		// 2. Restore Companies
		companyDirs, err := storage.ListCompanyDirs()
		if err != nil {
			return fmt.Errorf("failed to list companies: %w", err)
		}

		companyIDMap := map[int32]int32{}
		for _, shortName := range companyDirs {
			compMeta, err := storage.ReadCompany(shortName)
			if err != nil {
				log.Printf("Warning: failed to read company %s: %v", shortName, err)
				continue
			}

			oldID := compMeta.ID
			company := db.Company{
				Name:      compMeta.Name,
				ShortName: compMeta.ShortName,
				Color:     compMeta.Color,
			}
			if err := database.WithContext(ctx).Create(&company).Error; err != nil {
				log.Printf("Warning: failed to restore company %s: %v", shortName, err)
				continue
			}
			companyIDMap[oldID] = company.ID

			// 3. Restore Projects for this company
			projectDirs, err := storage.ListProjectDirs(shortName)
			if err != nil {
				log.Printf("Warning: failed to list projects for %s: %v", shortName, err)
				continue
			}

			projectIDMap := map[int32]int32{}
			for _, projName := range projectDirs {
				projMeta, err := storage.ReadProject(shortName, projName)
				if err != nil {
					log.Printf("Warning: failed to read project %s/%s: %v", shortName, projName, err)
					continue
				}

				oldProjID := projMeta.ID
				project := db.Project{
					CompanyID:       company.ID,
					Name:            projMeta.Name,
					Description:     projMeta.Description,
					WorkspaceFolder: projMeta.WorkspaceFolder,
					RepositoryUrl:   projMeta.RepositoryUrl,
				}
				if err := database.WithContext(ctx).Create(&project).Error; err != nil {
					log.Printf("Warning: failed to restore project %s: %v", projName, err)
					continue
				}
				projectIDMap[oldProjID] = project.ID
			}

			// 4. Restore Sprints for this company (read from companies/ Manager path)
			sprintRecords, err := fm.ListSprintsFromDisk(shortName)
			if err != nil {
				log.Printf("Warning: failed to read sprints for %s: %v", shortName, err)
				continue
			}

			sprintIDMap := map[int32]int32{}
			for _, s := range sprintRecords {
				oldSprintID := s.ID
				sprint := db.Sprint{
					CompanyID: company.ID,
					Name:      s.Name,
					Goal:      s.Goal,
					StartDate: s.StartDate,
					EndDate:   s.EndDate,
				}
				if err := database.WithContext(ctx).Create(&sprint).Error; err != nil {
					log.Printf("Warning: failed to restore sprint %s: %v", s.Name, err)
					continue
				}
				sprintIDMap[oldSprintID] = sprint.ID
			}

			// 5. Restore Agents for this company
			agents, err := storage.ReadAgents(shortName)
			if err != nil {
				log.Printf("Warning: failed to read agents for %s: %v", shortName, err)
				continue
			}

			agentIDMap := map[int32]int32{}
			for _, a := range agents {
				oldAgentID := a.ID
				agent := db.Agent{
					CompanyID:    company.ID,
					Name:         a.Name,
					Description:  a.Description,
					SystemPrompt: a.SystemPrompt,
					Model:        a.Model,
					Mode:         a.Mode,
					Permissions:  a.Permissions,
				}
				if a.ProviderID != nil {
					if newProviderID, ok := providerIDMap[*a.ProviderID]; ok {
						agent.ProviderID = &newProviderID
					}
				}
				if err := database.WithContext(ctx).Create(&agent).Error; err != nil {
					log.Printf("Warning: failed to restore agent %s: %v", a.Name, err)
					continue
				}
				agentIDMap[oldAgentID] = agent.ID
			}

			// 6. Restore Tasks for this company (read from companies/ Manager path)
			taskRecords, err := fm.ListTasksFromDisk(shortName)
			if err != nil {
				log.Printf("Warning: failed to read tasks for %s: %v", shortName, err)
				continue
			}

			taskIDMap := map[int32]int32{}
			for _, t := range taskRecords {
				oldTaskID := t.ID
				task := db.Task{
					CompanyID:   company.ID,
					Title:       t.Title,
					TaskType:    t.TaskType,
					Description: t.Description,
					Status:      t.Status,
					Priority:    t.Priority,
					IsArchived:  t.IsArchived,
					SprintID:    sprintIDMap[t.SprintID],
					DueDate:     t.DueDate,
				}
				if t.ProjectID != nil {
					if newProjectID, ok := projectIDMap[*t.ProjectID]; ok {
						task.ProjectID = &newProjectID
					}
				}
				if t.AgentID != nil {
					if newAgentID, ok := agentIDMap[*t.AgentID]; ok {
						task.AgentID = &newAgentID
					}
				}
				if t.ParentID != nil {
					if newParentID, ok := taskIDMap[*t.ParentID]; ok {
						task.ParentID = &newParentID
					}
				}
				if err := database.WithContext(ctx).Create(&task).Error; err != nil {
					log.Printf("Warning: failed to restore task %s: %v", t.Title, err)
					continue
				}
				taskIDMap[oldTaskID] = task.ID

				// 7. Restore Comments for this task (read from companies/ Manager path)
				commentRecs, err := fm.ListTaskCommentsFromDisk(shortName, t.ID)
				if err == nil {
					for _, c := range commentRecs {
						comment := db.Comment{
							TaskID:     task.ID,
							AuthorType: c.AuthorType,
							Content:    c.Content,
							AuthorID:   c.AuthorID,
						}
						database.WithContext(ctx).Create(&comment)
					}
				}

				// 8. Restore Attachments for this task
				attachments, err := storage.ReadAttachments(shortName, t.ID)
				if err == nil {
					for _, a := range attachments {
						attachment := db.Attachment{
							TaskID:   task.ID,
							Filename: a.Filename,
							FilePath: a.FilePath,
							MimeType: a.MimeType,
						}
						database.WithContext(ctx).Create(&attachment)
					}
				}

				// 9. Restore Runs for this task
				runs, err := storage.ReadRuns(shortName, t.ID)
				if err == nil {
					for _, r := range runs {
						run := db.Run{
							TaskID:     task.ID,
							AgentID:    agentIDMap[r.AgentID],
							Status:     r.Status,
							SessionID:  r.SessionID,
							LogContent: r.LogContent,
						}
						if r.StartedAt != "" {
							st, _ := parseTime(r.StartedAt)
							run.StartedAt = st
						}
						if r.EndedAt != "" {
							et, _ := parseTime(r.EndedAt)
							run.EndedAt = &et
						}
						database.WithContext(ctx).Create(&run)
					}
				}
			}
		}
	}

	// 10. Restore Activity Logs
	activityLogs, err := storage.ReadActivityLogs()
	if err == nil {
		for _, al := range activityLogs {
			activityLog := db.ActivityLog{
				CompanyID:  al.CompanyID,
				Action:     al.Action,
				EntityID:   al.EntityID,
				EntityType: al.EntityType,
				Details:    al.Details,
			}
			if al.CreatedAt != "" {
				t, _ := parseTime(al.CreatedAt)
				activityLog.CreatedAt = t
			}
			database.WithContext(ctx).Create(&activityLog)
		}
	}

	return nil
}

func parseTime(s string) (time.Time, error) {
	// Try multiple formats
	formats := []string{
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02 15:04:05",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("failed to parse time: %s", s)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(dstPath, data, 0644)
	})
}

func ListBackups(basePath string) ([]string, error) {
	backupDir := filepath.Join(basePath, "backups")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var backups []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".tar.gz") {
			backups = append(backups, entry.Name())
		}
	}
	return backups, nil
}
