package endpoints

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
	"github.com/go-chi/chi/v5"
)

func (api *API) ListTasks(w http.ResponseWriter, r *http.Request) {
	projIDStr := r.URL.Query().Get("project_id")
	if projIDStr == "" {
		api.respondError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	projID, _ := strconv.Atoi(projIDStr)

	query := api.db.Where("project_id = ?", projID)

	sprintIDStr := r.URL.Query().Get("sprint_id")
	if sprintIDStr != "" {
		sprintID, _ := strconv.Atoi(sprintIDStr)
		query = query.Where("sprint_id = ?", sprintID)
	}

	priority := r.URL.Query().Get("priority")
	if priority != "" {
		query = query.Where("priority = ?", priority)
	}

	agentIDStr := r.URL.Query().Get("agent_id")
	if agentIDStr != "" {
		agentID, _ := strconv.Atoi(agentIDStr)
		query = query.Where("agent_id = ?", agentID)
	}

	var tasks []db.Task
	if err := query.Order("id").Find(&tasks).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, tasks)
}

func (api *API) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID   int32   `json:"project_id"`
		AgentID     *int32  `json:"agent_id"`
		SprintID    *int32  `json:"sprint_id"`
		ParentID    *int32  `json:"parent_id"`
		Title       string  `json:"title"`
		Description string  `json:"description"`
		Priority    string  `json:"priority"`
		DueDate     *string `json:"due_date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	var dueDate *time.Time
	if req.DueDate != nil {
		t, _ := time.Parse(time.RFC3339, *req.DueDate)
		dueDate = &t
	}

	priority := req.Priority
	if priority == "" {
		priority = "Normal"
	}

	p := db.Task{
		ProjectID:   req.ProjectID,
		Title:       req.Title,
		Status:      "backlog",
		AgentID:     req.AgentID,
		SprintID:    req.SprintID,
		ParentID:    req.ParentID,
		Description: req.Description,
		Priority:    priority,
		DueDate:     dueDate,
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
