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
	IsLatest         bool
	parsedEntries    []interface{}
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
		IsLatest    bool          `json:"is_latest"`
		LogEntries  []interface{} `json:"log_entries"`
		TokenStats  interface{}   `json:"token_stats"`
	}{
		Alias:      Alias(r),
		IsLatest:   r.IsLatest,
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
		Preload("Task").
		Preload("Agent").
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
	resp := toRunResponse(run)
	// Mark is_latest so the frontend can show the Re-run button only on the
	// most recent run. Delegated child sessions are never re-runnable entry
	// points — only the main (root) session of a task can be re-run.
	if run.ParentRunID == nil {
		var maxID int64
		api.db.Model(&db.Run{}).Where("task_id = ?", run.TaskID).Select("MAX(id)").Scan(&maxID)
		resp.IsLatest = int64(run.ID) == maxID
	}
	api.respondJSON(w, http.StatusOK, resp)
}

// ListChildRuns returns the delegated session runs spawned by the given run,
// so the Run Log UI can render nested sessions.
func (api *API) ListChildRuns(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	runs, err := api.q.ListChildRuns(r.Context(), int32(id))
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

func (api *API) RerunTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	task, err := api.q.GetTask(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "task not found")
		return
	}

	// Subtasks are delegated sessions owned by the orchestrator — re-running
	// one in isolation would spawn an orphan session outside the main flow.
	// Walk up to the root task so a re-run always restarts the main session.
	for task.ParentID != nil {
		parent, perr := api.q.GetTask(r.Context(), *task.ParentID)
		if perr != nil {
			break
		}
		task = parent
	}

	if task.AgentID == nil {
		api.respondError(w, http.StatusBadRequest, "task has no assigned agent")
		return
	}
	if err := api.engine.RerunTask(r.Context(), task.ID); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]interface{}{"status": "queued", "task_id": task.ID})
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
