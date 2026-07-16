package endpoints

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"

	"agent-orchestrator/db"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// RunResponse is the wire shape returned to the frontend. It is identical
// to db.Run except that token_stats is exposed as a parsed object rather
// than a stringified blob, so the Run Log viewer can render the header
// breakdown without an extra round trip. Log entries are NOT embedded —
// the frontend fetches them from GET /runs/{id}/log.
type RunResponse struct {
	db.Run
	IsLatest         bool
	parsedTokenStats interface{}
}

// MarshalJSON promotes token_stats from a stringified blob to a parsed
// object.
func (r RunResponse) MarshalJSON() ([]byte, error) {
	type Alias RunResponse // avoid infinite recursion
	tokenStats := r.parsedTokenStats
	if tokenStats == nil {
		tokenStats = map[string]interface{}{}
	}
	return json.Marshal(&struct {
		Alias
		IsLatest   bool        `json:"is_latest"`
		TokenStats interface{} `json:"token_stats"`
	}{
		Alias:      Alias(r),
		IsLatest:   r.IsLatest,
		TokenStats: tokenStats,
	})
}

func toRunResponse(run db.Run) RunResponse {
	resp := RunResponse{Run: run}
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

	// The list view only needs overview info. Task/Agent preloads are
	// trimmed to the handful of fields the list actually renders.
	var runs []db.Run
	err := api.db.
		Preload("Task", func(tx *gorm.DB) *gorm.DB { return tx.Select("id", "ref_key", "title") }).
		Preload("Agent", func(tx *gorm.DB) *gorm.DB { return tx.Select("id", "name") }).
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

// maxHydratedEntryBytes caps how much of a single entry's content is
// hydrated into the list response; the UI can fetch the full entry from
// GET /runs/{id}/log/{seq} when it needs more.
const maxHydratedEntryBytes = 512 * 1024

// GetRunLog returns the run's log entries in order. With ?full=0 only the
// stored metadata (type, ts, preview, token counts) is returned; by default
// each entry's full content is hydrated from the run's JSONL log file via
// the stored byte offsets. The response shape matches the legacy embedded
// log_entries array (plus a "seq" field), so the frontend renderer and the
// live WebSocket stream stay compatible.
func (api *API) GetRunLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	entries, err := api.q.ListRunLogEntries(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	full := r.URL.Query().Get("full") != "0"
	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()

	out := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		out = append(out, hydrateEntry(e, full, maxHydratedEntryBytes, files))
	}
	api.respondJSON(w, http.StatusOK, map[string]interface{}{"entries": out})
}

// GetRunLogEntry returns one fully-hydrated log entry (no size cap).
func (api *API) GetRunLogEntry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	seq, err := strconv.Atoi(chi.URLParam(r, "seq"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid seq")
		return
	}
	entry, err := api.q.GetRunLogEntry(r.Context(), int32(id), int32(seq))
	if err != nil {
		api.respondError(w, http.StatusNotFound, err.Error())
		return
	}
	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	api.respondJSON(w, http.StatusOK, hydrateEntry(entry, true, 0, files))
}

// hydrateEntry converts a metadata row into the wire entry map. When full
// is true and the row points into a JSONL file, the original entry line is
// read back via its byte offset (sharing open file handles through files).
// maxBytes > 0 caps how much content is returned. Falls back to the stored
// metadata when the file is missing or unreadable.
func hydrateEntry(e db.RunLogEntry, full bool, maxBytes int, files map[string]*os.File) map[string]interface{} {
	if full && e.LogFilePath != "" && e.ByteLen > 0 {
		f, ok := files[e.LogFilePath]
		if !ok {
			var err error
			f, err = os.Open(e.LogFilePath)
			if err == nil {
				files[e.LogFilePath] = f
			}
		}
		if f != nil {
			buf := make([]byte, e.ByteLen)
			if _, err := f.ReadAt(buf, e.ByteOffset); err == nil {
				var entry map[string]interface{}
				if json.Unmarshal(buf, &entry) == nil {
					entry["seq"] = e.Seq
					// The metadata row's prompt_tokens may have been patched
					// after the line was written (SetFirstRequestPromptTokens).
					if e.PromptTokens > 0 {
						entry["prompt_tokens"] = e.PromptTokens
					}
					if maxBytes > 0 {
						if content, ok := entry["content"].(string); ok && len(content) > maxBytes {
							entry["content"] = content[:maxBytes]
							entry["content_truncated"] = true
						}
					}
					return entry
				}
			}
		}
	}

	// Metadata-only fallback (also used for ?full=0 and file-less entries).
	entry := map[string]interface{}{
		"seq":     e.Seq,
		"type":    e.Type,
		"content": e.Preview,
		"ts":      e.Ts.UTC().Format(time.RFC3339Nano),
	}
	if e.ToolName != "" {
		entry["tool_name"] = e.ToolName
	}
	if e.Model != "" {
		entry["model"] = e.Model
	}
	if e.AgentName != "" {
		entry["agent_name"] = e.AgentName
	}
	if e.StatusCode != 0 {
		entry["status_code"] = e.StatusCode
	}
	if e.PromptTokens != 0 {
		entry["prompt_tokens"] = e.PromptTokens
	}
	if e.InputTokens != 0 {
		entry["input_tokens"] = e.InputTokens
	}
	if e.OutputTokens != 0 {
		entry["output_tokens"] = e.OutputTokens
	}
	if e.ChildRunID != nil {
		entry["run_id"] = *e.ChildRunID
	}
	return entry
}

// ListChildRuns returns the delegated session runs spawned by the given run,
// so the Run Log UI can render nested sessions. With ?deep=true it returns
// every descendant session in the run's tree (children, grandchildren, …),
// which the UI uses for whole-tree per-agent token stats.
func (api *API) ListChildRuns(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var runs []db.Run
	if r.URL.Query().Get("deep") == "true" {
		// Descendants are resolved via root_run_id, which only works for root
		// runs (they point at themselves); for child sessions fall back to
		// direct children.
		runs, err = api.q.ListDescendantRuns(r.Context(), int32(id))
		if err == nil && len(runs) == 0 {
			runs, err = api.q.ListChildRuns(r.Context(), int32(id))
		}
	} else {
		runs, err = api.q.ListChildRuns(r.Context(), int32(id))
	}
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
