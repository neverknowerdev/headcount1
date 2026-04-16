package endpoints

import (
	"encoding/json"
	"net/http"
	"strconv"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
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
		CompanyID   int32  `json:"company_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	p := db.Project{
		CompanyID:   req.CompanyID,
		Name:        req.Name,
		Description: req.Description,
	}

	proj, err := api.q.CreateProject(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var comp db.Company
	api.db.First(&comp, req.CompanyID)

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	fsManager.CreateProjectDirectories(comp, proj)

	api.logActivity(req.CompanyID, "project_created", int32(proj.ID), "project", "")

	api.respondJSON(w, http.StatusCreated, proj)
}
