package endpoints

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
		if err := validateGitURL(req.RepositoryUrl); err != nil {
			api.respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		settings := LoadSettings()
		sshDir := filepath.Join(settings.BasePath, ".ssh")
		gitMgr := git.NewGitManager("", sshDir)
		if err := gitMgr.ValidateRemote(r.Context(), req.RepositoryUrl); err != nil {
			api.respondError(w, http.StatusBadRequest, "Cannot access git repository: "+err.Error())
			return
		}
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

	// Write project metadata to filesystem
	storage := filesystem.NewStorage(settings.BasePath)
	if err := storage.WriteProject(comp, proj); err != nil {
		log.Printf("Warning: failed to write project metadata: %v", err)
	}

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
	if _, err := api.q.CreateMCPServer(r.Context(), cgServer); err != nil {
		log.Printf("Warning: failed to create codegraph MCP server for project %d: %v", proj.ID, err)
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
		if err := validateGitURL(req.RepositoryUrl); err != nil {
			api.respondError(w, http.StatusBadRequest, err.Error())
			return
		}

		settings := LoadSettings()
		sshDir := filepath.Join(settings.BasePath, ".ssh")
		gitMgr := git.NewGitManager("", sshDir)
		if err := gitMgr.ValidateRemote(r.Context(), req.RepositoryUrl); err != nil {
			api.respondError(w, http.StatusBadRequest, "Cannot access git repository: "+err.Error())
			return
		}

		project.RepositoryUrl = req.RepositoryUrl

		var comp db.Company
		api.db.First(&comp, project.CompanyID)
		fsManager := filesystem.NewManager(settings.BasePath)
		if err := fsManager.PrepareProjectRepo(r.Context(), comp, project); err != nil {
			api.respondError(w, http.StatusInternalServerError, "Failed to prepare project repo: "+err.Error())
			return
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

func validateGitURL(url string) error {
	if !strings.HasSuffix(url, ".git") &&
		!strings.HasPrefix(url, "file://") &&
		!strings.HasPrefix(url, "/") &&
		!strings.Contains(url, "github.com:") &&
		!strings.Contains(url, "gitlab.com:") &&
		!strings.Contains(url, "bitbucket.org:") &&
		!strings.Contains(url, "ssh://") {
		return fmt.Errorf("invalid git URL: must end with .git, be a file:// path, or be a recognized git hosting URL")
	}
	return nil
}
