package endpoints

import (
	"encoding/json"
	"net/http"
	"strconv"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
	"github.com/go-chi/chi/v5"
)

func (api *API) ListTasks(w http.ResponseWriter, r *http.Request) {
	projID, err := strconv.Atoi(r.URL.Query().Get("project_id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	tasks, err := api.q.ListTasksByProject(r.Context(), int32(projID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, tasks)
}

func (api *API) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID   int32  `json:"project_id"`
		AgentID     *int32 `json:"agent_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	p := db.Task{
		ProjectID:   req.ProjectID,
		Title:       req.Title,
		Status:      "backlog",
		AgentID:     req.AgentID,
		Description: req.Description,
	}

	task, err := api.q.CreateTask(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.hub.BroadcastEvent("task_created", task)

	var proj db.Project
	api.db.First(&proj, req.ProjectID)
	var comp db.Company
	api.db.First(&comp, proj.CompanyID)

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	fsManager.CreateTaskWorkspace(comp, proj, task)

	api.logActivity(comp.ID, "task_created", int32(task.ID), "task", "")

	api.respondJSON(w, http.StatusCreated, task)
}

func (api *API) GetTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	task, err := api.q.GetTask(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, task)
}

func (api *API) UpdateTaskStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	task, err := api.q.UpdateTaskStatus(r.Context(), int32(id), req.Status)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.hub.BroadcastEvent("task_updated", task)

	var proj db.Project
	api.db.First(&proj, task.ProjectID)

	api.logActivity(proj.CompanyID, "task_status_updated", int32(task.ID), "task", `{"status":"`+req.Status+`"}`)

	go api.engine.ProcessTask(r.Context(), int32(id))

	api.respondJSON(w, http.StatusOK, task)
}
