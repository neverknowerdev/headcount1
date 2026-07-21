package endpoints

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"agent-orchestrator/db"
)

func (api *API) ListSprints(w http.ResponseWriter, r *http.Request) {
	compIDStr := r.URL.Query().Get("company_id")
	if compIDStr == "" {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}

	compID, err := strconv.Atoi(compIDStr)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid company_id")
		return
	}
	if _, err := api.authorizeCompany(r, int32(compID)); err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}

	sprints, err := api.q.ListSprintsByCompany(r.Context(), int32(compID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, sprints)
}

func (api *API) CreateSprint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID int32   `json:"company_id"`
		Name      string  `json:"name"`
		Goal      string  `json:"goal"`
		StartDate *string `json:"start_date"`
		EndDate   *string `json:"end_date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	if _, err := api.authorizeCompany(r, req.CompanyID); err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}

	var startDate, endDate *time.Time
	if req.StartDate != nil {
		t, _ := time.Parse(time.RFC3339, *req.StartDate)
		startDate = &t
	}
	if req.EndDate != nil {
		t, _ := time.Parse(time.RFC3339, *req.EndDate)
		endDate = &t
	}

	s := db.Sprint{
		CompanyID: req.CompanyID,
		Name:      req.Name,
		Goal:      req.Goal,
		StartDate: startDate,
		EndDate:   endDate,
	}

	sprint, err := api.q.CreateSprint(r.Context(), s)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.logActivity(req.CompanyID, "sprint_created", int32(sprint.ID), "sprint", "")
	api.respondJSON(w, http.StatusCreated, sprint)
}
