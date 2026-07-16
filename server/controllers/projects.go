package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/git"

	"github.com/go-chi/chi/v5"
)

func (api *API) ListProjects(w http.ResponseWriter, r *http.Request) {
	compID, err := strconv.Atoi(r.URL.Query().Get("company_id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	projects, err := api.q.ListProjectsByCompany(r.Context(), int32(compID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, projects)
}

func (api *API) CreateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID       int32  `json:"company_id"`
		Name            string `json:"name"`
		Description     string `json:"description"`
		WorkspaceFolder string `json:"workspace_folder"`
		RepositoryUrl   string `json:"repository_url"`
		IsExternal      bool   `json:"is_external"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	// External projects require a git URL to clone from.
	if req.IsExternal && req.RepositoryUrl == "" {
		api.respondError(w, http.StatusBadRequest, "external projects require repository_url")
		return
	}

	if req.RepositoryUrl != "" {
		settings := LoadSettings()
		sshDir := filepath.Join(settings.BasePath, ".ssh")
		normalized, err := validateAndConnectRepo(r.Context(), req.RepositoryUrl, sshDir)
		if err != nil {
			api.respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.RepositoryUrl = normalized
	}

	p := db.Project{
		CompanyID:       req.CompanyID,
		Name:            req.Name,
		Description:     req.Description,
		WorkspaceFolder: req.WorkspaceFolder,
		RepositoryUrl:   req.RepositoryUrl,
		IsExternal:      req.IsExternal,
	}

	var comp db.Company
	api.db.First(&comp, req.CompanyID)

	settings := LoadSettings()

	if req.WorkspaceFolder == "" {
		req.WorkspaceFolder = comp.ShortName + "/" + req.Name
	}
	p.WorkspaceFolder = req.WorkspaceFolder

	proj, err := api.q.CreateProject(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if req.WorkspaceFolder != "" {
		// Add the workspace folder to settings
		found := false
		for _, f := range settings.WorkspaceFolders {
			if f == req.WorkspaceFolder {
				found = true
				break
			}
		}
		if !found {
			settings.WorkspaceFolders = append(settings.WorkspaceFolders, req.WorkspaceFolder)
			SaveSettings(settings)
		}
	}

	fsManager := filesystem.NewManager(settings.BasePath)
	fsManager.CreateProjectDirectories(comp, proj)

	if req.RepositoryUrl != "" {
		if err := fsManager.PrepareProjectRepo(r.Context(), comp, proj); err != nil {
			api.respondError(w, http.StatusInternalServerError, "Failed to prepare project repo: "+err.Error())
			return
		}
	}

	// Auto-create a codegraph MCP server for every project so agents can run
	// semantic code analysis. The server is disabled by default; agents or users
	// enable it explicitly. WorkDir is set to the project repo path so codegraph
	// finds its SQLite knowledge graph.
	repoPath := fsManager.GetProjectRepoPath(comp, proj)
	cgID := proj.ID
	cgServer := db.MCPServer{
		Name:        fmt.Sprintf("codegraph-%d", proj.ID),
		DisplayName: fmt.Sprintf("CodeGraph: %s", proj.Name),
		Description: fmt.Sprintf("Semantic code intelligence for project %q. Run codegraph_init once to build the knowledge graph.", proj.Name),
		Transport:   "stdio",
		Command:     "codegraph",
		Args:        `["serve","--mcp"]`,
		WorkDir:     repoPath,
		ProjectID:   &cgID,
		Enabled:     false,
	}
	newCGServer, cgErr := api.q.CreateMCPServer(r.Context(), cgServer)
	if cgErr != nil {
		log.Printf("Warning: failed to create codegraph MCP server for project %d: %v", proj.ID, cgErr)
	} else if req.RepositoryUrl != "" {
		api.startCodegraphInit(proj.ID, newCGServer.ID, repoPath)
	}

	api.logActivity(req.CompanyID, "project_created", int32(proj.ID), "project", "")

	api.respondJSON(w, http.StatusCreated, proj)
}

func (api *API) GetProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	project, err := api.q.GetProject(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "Project not found")
		return
	}
	api.respondJSON(w, http.StatusOK, project)
}

func (api *API) UpdateProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req struct {
		Name            string `json:"name"`
		Description     string `json:"description"`
		WorkspaceFolder string `json:"workspace_folder"`
		RepositoryUrl   string `json:"repository_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	project, err := api.q.GetProject(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "Project not found")
		return
	}

	if req.Name != "" {
		project.Name = req.Name
	}
	if req.Description != "" {
		project.Description = req.Description
	}
	if req.WorkspaceFolder != "" {
		project.WorkspaceFolder = req.WorkspaceFolder
	}

	if req.RepositoryUrl != "" {
		settings := LoadSettings()
		sshDir := filepath.Join(settings.BasePath, ".ssh")
		normalized, err := validateAndConnectRepo(r.Context(), req.RepositoryUrl, sshDir)
		if err != nil {
			api.respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		project.RepositoryUrl = normalized

		var comp db.Company
		api.db.First(&comp, project.CompanyID)
		fsManager := filesystem.NewManager(settings.BasePath)
		if err := fsManager.PrepareProjectRepo(r.Context(), comp, project); err != nil {
			api.respondError(w, http.StatusInternalServerError, "Failed to prepare project repo: "+err.Error())
			return
		}

		// Ensure a codegraph MCP server exists for this project, creating one if
		// the project pre-dates the auto-create logic. Then kick off init.
		repoPath := fsManager.GetProjectRepoPath(comp, project)
		cgSrv, cgErr := api.q.GetCodegraphServerForProject(r.Context(), project.ID)
		if cgErr != nil {
			// Server missing — create it now.
			cgID := project.ID
			cgSrv, cgErr = api.q.CreateMCPServer(r.Context(), db.MCPServer{
				Name:        fmt.Sprintf("codegraph-%d", project.ID),
				DisplayName: fmt.Sprintf("CodeGraph: %s", project.Name),
				Description: fmt.Sprintf("Semantic code intelligence for project %q.", project.Name),
				Transport:   "stdio",
				Command:     "codegraph",
				Args:        `["serve","--mcp"]`,
				WorkDir:     repoPath,
				ProjectID:   &cgID,
				Enabled:     false,
			})
			if cgErr != nil {
				log.Printf("Warning: failed to create codegraph server for project %d: %v", project.ID, cgErr)
			}
		} else if cgSrv.WorkDir != repoPath {
			// Repo path changed — sync WorkDir so codegraph finds the right index.
			cgSrv.WorkDir = repoPath
			if updated, err := api.q.UpdateMCPServer(r.Context(), cgSrv); err == nil {
				cgSrv = updated
			}
		}
		if cgErr == nil {
			api.startCodegraphInit(project.ID, cgSrv.ID, repoPath)
		}
	}

	project, err = api.q.UpdateProject(r.Context(), project)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.logActivity(project.CompanyID, "project_updated", int32(project.ID), "project", "")
	api.respondJSON(w, http.StatusOK, project)
}

// GetProjectCodegraph returns the codegraph MCP server record for a project,
// including its init_status so the frontend can poll for readiness.
func (api *API) GetProjectCodegraph(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	srv, err := api.q.GetCodegraphServerForProject(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "no codegraph server for this project")
		return
	}
	api.respondJSON(w, http.StatusOK, srv)
}

// startCodegraphInit marks the codegraph server as "initializing" and runs
// "codegraph init" in the repo directory as a background goroutine. On
// success it sets init_status to "ready" and populates tools_cache.
// It is idempotent: if the server is already "ready" or "initializing" it
// returns immediately without launching a second process.
func (api *API) startCodegraphInit(projectID, serverID int32, repoPath string) {
	ctx := context.Background()

	// Idempotency guard — re-read current status before committing.
	if current, err := api.q.GetCodegraphServerForProject(ctx, projectID); err == nil {
		if current.InitStatus == "ready" || current.InitStatus == "initializing" {
			log.Printf("codegraph: project %d already %s, skipping init", projectID, current.InitStatus)
			return
		}
	}

	if err := api.q.UpdateMCPServerInitStatus(ctx, serverID, "initializing"); err != nil {
		log.Printf("codegraph: set initializing failed: %v", err)
	}
	go func() {
		log.Printf("codegraph init: starting for project %d in %s", projectID, repoPath)
		cmd := exec.Command("codegraph", "init")
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := fmt.Sprintf("error: %v — %s", err, strings.TrimSpace(string(out)))
			log.Printf("codegraph init: failed for project %d: %s", projectID, msg)
			_ = api.q.UpdateMCPServerInitStatus(ctx, serverID, msg)
			return
		}
		log.Printf("codegraph init: completed for project %d", projectID)
		_ = api.q.UpdateMCPServerInitStatus(ctx, serverID, "ready")

		// Discover and cache tools now that the knowledge graph is ready.
		srv, err := api.q.GetCodegraphServerForProject(ctx, projectID)
		if err != nil {
			return
		}
		toolsJSON, discErr := discoverServerTools(ctx, srv)
		if discErr != nil {
			log.Printf("codegraph: tool discovery failed for project %d: %v", projectID, discErr)
			return
		}
		_ = api.q.UpdateMCPServerToolsCache(ctx, srv.ID, toolsJSON)
		log.Printf("codegraph: tools cached for project %d", projectID)
	}()
}

// InitPendingCodegraphServers finds every codegraph server that is not yet
// "ready", checks that the repo directory exists on disk, and kicks off
// startCodegraphInit for each one. Intended to run once at startup.
func (api *API) InitPendingCodegraphServers(ctx context.Context) {
	pairs, err := api.q.ListPendingCodegraphServers(ctx)
	if err != nil {
		log.Printf("codegraph startup sweep: failed to list pending: %v", err)
		return
	}
	if len(pairs) == 0 {
		return
	}
	log.Printf("codegraph startup sweep: %d project(s) pending init", len(pairs))
	for _, pair := range pairs {
		if pair.Server.WorkDir == "" {
			continue
		}
		if _, statErr := os.Stat(pair.Server.WorkDir); os.IsNotExist(statErr) {
			log.Printf("codegraph startup: repo dir not on disk yet for project %d, skipping", pair.Project.ID)
			continue
		}
		api.startCodegraphInit(pair.Project.ID, pair.Server.ID, pair.Server.WorkDir)
	}
}

// normalizeGitURL converts user-friendly repo URLs into SSH git URLs.
// e.g. "github.com/user/repo" → "git@github.com:user/repo.git"
func normalizeGitURL(rawURL string) string {
	// Already in SSH or local format — keep as-is.
	if strings.HasPrefix(rawURL, "git@") || strings.HasPrefix(rawURL, "ssh://") ||
		strings.HasPrefix(rawURL, "file://") || strings.HasPrefix(rawURL, "/") {
		return rawURL
	}

	// Strip https:// or http:// prefix if present.
	stripped := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")

	// Convert known git hosting services to SSH format.
	for _, host := range []string{"github.com", "gitlab.com", "bitbucket.org"} {
		if strings.HasPrefix(stripped, host+"/") {
			path := strings.TrimPrefix(stripped, host+"/")
			path = strings.TrimSuffix(path, ".git")
			return fmt.Sprintf("git@%s:%s.git", host, path)
		}
	}

	return rawURL
}

func validateGitURL(url string) error {
	if strings.HasPrefix(url, "git@") ||
		strings.HasPrefix(url, "ssh://") ||
		strings.HasPrefix(url, "file://") ||
		strings.HasPrefix(url, "/") ||
		strings.HasSuffix(url, ".git") {
		return nil
	}
	return fmt.Errorf("invalid repository URL — use a format like: github.com/user/repo or git@github.com:user/repo.git")
}

// validateAndConnectRepo normalizes the URL, validates its format, then tests
// remote connectivity. Returns the normalized URL on success.
func validateAndConnectRepo(ctx context.Context, rawURL, sshDir string) (string, error) {
	normalized := normalizeGitURL(rawURL)
	if err := validateGitURL(normalized); err != nil {
		return "", err
	}

	sshKeyPath := filepath.Join(sshDir, "id_rsa")
	_, sshKeyStatErr := os.Stat(sshKeyPath)
	sshKeyExists := sshKeyStatErr == nil

	gitMgr := git.NewGitManager("", sshDir)
	if err := gitMgr.ValidateRemote(ctx, normalized); err != nil {
		if strings.HasPrefix(err.Error(), "[auth]") {
			if !sshKeyExists {
				return "", fmt.Errorf("this repository requires authentication — configure your SSH key in Settings first")
			}
			return "", fmt.Errorf("SSH authentication failed — ensure the SSH key in Settings has read access to this repository")
		}
		return "", fmt.Errorf("cannot access repository: %s", err.Error())
	}
	return normalized, nil
}
