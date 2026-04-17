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

import "strings"

func (api *API) ListTasks(w http.ResponseWriter, r *http.Request) {
	compIDStr := r.URL.Query().Get("company_id")
	if compIDStr == "" {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	compID, _ := strconv.Atoi(compIDStr)

	query := api.db.Where("company_id = ?", compID)

	projIDsStr := r.URL.Query().Get("project_ids")
	if projIDsStr != "" {
		ids := strings.Split(projIDsStr, ",")
		query = query.Where("project_id IN ?", ids)
	}

	sprintIDsStr := r.URL.Query().Get("sprint_ids")
	if sprintIDsStr != "" {
		ids := strings.Split(sprintIDsStr, ",")
		query = query.Where("sprint_id IN ?", ids)
	}

	archivedStr := r.URL.Query().Get("archived")
	if archivedStr == "true" {
		query = query.Where("is_archived = ?", true)
	} else {
		query = query.Where("is_archived = ?", false)
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
		CompanyID   int32   `json:"company_id"`
		ProjectID   *int32  `json:"project_id"`
		AgentID     *int32  `json:"agent_id"`
		SprintID    int32   `json:"sprint_id"`
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

	if req.CompanyID == 0 {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}

	p := db.Task{
		CompanyID:   req.CompanyID,
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

	var comp db.Company
	api.db.First(&comp, req.CompanyID)

	if req.ProjectID != nil {
		var proj db.Project
		api.db.First(&proj, *req.ProjectID)
		settings := LoadSettings()
		fsManager := filesystem.NewManager(settings.BasePath)
		fsManager.CreateTaskWorkspace(comp, proj, task)
	}

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

func (api *API) UpdateTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		ProjectID   *int32  `json:"project_id"`
		AgentID     *int32  `json:"agent_id"`
		SprintID    *int32  `json:"sprint_id"`
		ParentID    *int32  `json:"parent_id"`
		Title       string  `json:"title"`
		Description string  `json:"description"`
		Priority    string  `json:"priority"`
		DueDate     *string `json:"due_date"`
		Status      string  `json:"status"`
		IsArchived  *bool   `json:"is_archived"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	task, err := api.q.GetTask(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "Task not found")
		return
	}

	statusChanged := false
	if req.Status != "" && req.Status != task.Status {
		task.Status = req.Status
		statusChanged = true
	}
	if req.Title != "" {
		task.Title = req.Title
	}
	if req.Description != "" {
		task.Description = req.Description
	}
	if req.Priority != "" {
		task.Priority = req.Priority
	}

	if req.ProjectID != nil {
		task.ProjectID = req.ProjectID
	}
	if req.AgentID != nil {
		task.AgentID = req.AgentID
	}
	if req.SprintID != nil {
		task.SprintID = *req.SprintID
	}
	if req.ParentID != nil {
		task.ParentID = req.ParentID
	}
	if req.IsArchived != nil {
		task.IsArchived = *req.IsArchived
	}

	if req.DueDate != nil {
		t, _ := time.Parse(time.RFC3339, *req.DueDate)
		task.DueDate = &t
	}

	task, err = api.q.UpdateTask(r.Context(), task)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.hub.BroadcastEvent("task_updated", task)

	api.logActivity(task.CompanyID, "task_updated", int32(task.ID), "task", "")

	if statusChanged {
		go api.engine.ProcessTask(r.Context(), int32(id))
	}

	api.respondJSON(w, http.StatusOK, task)
}
