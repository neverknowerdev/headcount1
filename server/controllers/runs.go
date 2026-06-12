package endpoints

import (
	"encoding/json"
	"net/http"
	"strconv"

	"agent-orchestrator/db"

	"github.com/go-chi/chi/v5"
)

// RunResponse is the wire shape returned to the frontend. It is identical to
// db.Run except that log_entries is exposed as a parsed JSON array rather
// than a stringified blob — so the frontend can call .slice()/.map() on it
// directly without an extra JSON.parse step. token_stats is exposed the
// same way so the Run Log viewer can render the header breakdown without
// an extra round trip to /runs/{id}/token-stats.
type RunResponse struct {
	db.Run
	parsedEntries  []interface{}
	parsedTokenStats interface{}
}

// MarshalJSON renders log_entries as a parsed JSON array, matching what the
// frontend expects when it calls .slice()/.map() on the field. It also
// promotes token_stats from a stringified blob to a parsed object.
func (r RunResponse) MarshalJSON() ([]byte, error) {
	type Alias RunResponse // avoid infinite recursion
	entries := r.parsedEntries
	if entries == nil {
		entries = []interface{}{}
	}
	tokenStats := r.parsedTokenStats
	if tokenStats == nil {
		tokenStats = map[string]interface{}{}
	}
	return json.Marshal(&struct {
		Alias
		LogEntries  []interface{} `json:"log_entries"`
		TokenStats  interface{}   `json:"token_stats"`
	}{
		Alias:      Alias(r),
		LogEntries: entries,
		TokenStats: tokenStats,
	})
}

func toRunResponse(run db.Run) RunResponse {
	resp := RunResponse{Run: run}
	if run.LogEntries != "" {
		_ = json.Unmarshal([]byte(run.LogEntries), &resp.parsedEntries)
	}
	if run.TokenStats != "" {
		_ = json.Unmarshal([]byte(run.TokenStats), &resp.parsedTokenStats)
	}
	return resp
}

func (api *API) ListCompanyRuns(w http.ResponseWriter, r *http.Request) {
	compIDStr := r.URL.Query().Get("company_id")
	if compIDStr == "" {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	compID, _ := strconv.Atoi(compIDStr)

	// Fetch all tasks for company
	var taskIDs []int32
	api.db.Table("tasks").Where("company_id = ?", compID).Pluck("id", &taskIDs)

	if len(taskIDs) == 0 {
		api.respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	var runs []db.Run
	err := api.db.
		Where("task_id IN ?", taskIDs).
		Order("started_at desc").
		Find(&runs).Error

	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]RunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunResponse(run))
	}
	api.respondJSON(w, http.StatusOK, out)
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
	api.respondJSON(w, http.StatusOK, toRunResponse(run))
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
	api.respondJSON(w, http.StatusOK, toRunResponse(run))
}

func (api *API) StopRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	run, err := api.q.GetRun(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "Run not found")
		return
	}

	if run.Status != "running" {
		api.respondError(w, http.StatusBadRequest, "Run is not in progress")
		return
	}

	api.engine.StopRun(r.Context(), int32(id))

	api.respondJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
}
