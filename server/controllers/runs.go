package endpoints

import (
	"net/http"
	"strconv"

	"agent-orchestrator/db"
	"github.com/go-chi/chi/v5"
)

func (api *API) ListCompanyRuns(w http.ResponseWriter, r *http.Request) {
	compIDStr := r.URL.Query().Get("company_id")
	if compIDStr == "" {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	compID, _ := strconv.Atoi(compIDStr)

	var runs []db.Run
	if err := api.db.Preload("Agent").Preload("Task").Joins("JOIN tasks ON tasks.id = runs.task_id").Where("tasks.company_id = ?", compID).Order("runs.started_at desc").Find(&runs).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, runs)
}

func (api *API) GetRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	run, err := api.q.GetRun(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, run)
}

func (api *API) GetRunBySessionID(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		api.respondError(w, http.StatusBadRequest, "sessionID is required")
		return
	}
	run, err := api.q.GetRunBySessionID(r.Context(), sessionID)
	if err != nil {
		api.respondError(w, http.StatusNotFound, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, run)
}

func (api *API) RerunRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	run, err := api.q.GetRun(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "run not found")
		return
	}

	// Deduplication: check if there's already a running task for this task ID
	var existingRun db.Run
	if err := api.db.Where("task_id = ? AND status = ?", run.TaskID, "running").First(&existingRun).Error; err == nil {
		api.respondError(w, http.StatusConflict, "a run is already in progress for this task")
		return
	}

	mode := run.Mode
	if mode == "" {
		mode = "implement"
	}
	if err := api.engine.ReRunTask(r.Context(), run.TaskID, mode); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusAccepted, map[string]string{"status": "re-run started"})
}
