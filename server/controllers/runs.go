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

	var run db.Run
	if err := api.db.Preload("Agent").Preload("Task").First(&run, id).Error; err != nil {
		api.respondError(w, http.StatusNotFound, "run not found")
		return
	}

	api.respondJSON(w, http.StatusOK, run)
}
