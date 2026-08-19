package endpoints

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/pkg/filesystem"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// RunResponse is the wire shape returned to the frontend. It is identical to
// db.Run except that log_entries is exposed as a parsed JSON array rather
// than a stringified blob — so the frontend can call .slice()/.map() on it
// directly without an extra JSON.parse step. token_stats is exposed the
// same way so the Run Log viewer can render the header breakdown without
// an extra round trip to /runs/{id}/token-stats.
type RunResponse struct {
	db.Run
	// AgentName is the UI identity of this session. An orchestrator is a
	// control-plane session even when it executes with the CEO agent's model
	// configuration, so it must not be presented as the CEO.
	AgentName        string `json:"agent_name"`
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
		IsLatest   bool          `json:"is_latest"`
		LogEntries []interface{} `json:"log_entries"`
		TokenStats interface{}   `json:"token_stats"`
	}{
		Alias:      Alias(r),
		IsLatest:   r.IsLatest,
		LogEntries: entries,
		TokenStats: tokenStats,
	})
}

func toRunResponse(run db.Run) RunResponse {
	agentName := run.Agent.Name
	if run.Kind == db.RunKindTaskOrchestrator {
		agentName = "Orchestrator"
	}
	resp := RunResponse{Run: run, AgentName: agentName}
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
	if _, err := api.authorizeCompany(r, int32(compID)); err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}

	// Fetch all tasks for company
	var taskIDs []int32
	api.db.Table("tasks").Where("company_id = ?", compID).Pluck("id", &taskIDs)

	if len(taskIDs) == 0 {
		api.respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	// The list view only needs overview info: log_content/log_entries are
	// full transcripts (can be megabytes for long sessions) and are only
	// ever rendered on the Run Log Details page, so they're omitted here to
	// keep the list fast and responsive as run history grows. Task/Agent
	// preloads are similarly trimmed to the handful of fields the list
	// actually renders.
	var runs []db.Run
	err := api.db.
		Omit("log_content", "log_entries").
		Preload("Task", func(tx *gorm.DB) *gorm.DB { return tx.Select("id", "ref_key", "title", "orchestrator_run_id") }).
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
	run := api.runFromCtx(r) // loaded + authorized by LoadRun
	resp := toRunResponse(run)
	// Mark is_latest so the frontend can show the Re-run button only on the
	// most recent run. Delegated child sessions are never re-runnable entry
	// points — only the main (root) session of a task can be re-run.
	isOrchestrator := run.Task.OrchestratorRunID != nil && *run.Task.OrchestratorRunID == run.ID
	if run.ParentRunID == nil && !isOrchestrator {
		var maxID int64
		api.db.Model(&db.Run{}).Where("task_id = ?", run.TaskID).Select("MAX(id)").Scan(&maxID)
		resp.IsLatest = int64(run.ID) == maxID
	}
	api.respondJSON(w, http.StatusOK, resp)
}

// ListChildRuns returns the delegated session runs spawned by the given run,
// so the Run Log UI can render nested sessions. With ?deep=true it returns
// every descendant session in the run's tree (children, grandchildren, …),
// which the UI uses for whole-tree per-agent token stats.
func (api *API) ListChildRuns(w http.ResponseWriter, r *http.Request) {
	run := api.runFromCtx(r) // loaded + authorized by LoadRun
	id := run.ID
	var runs []db.Run
	var err error
	if r.URL.Query().Get("deep") == "true" {
		// Descendants are resolved via root_run_id, which only works for root
		// runs (they point at themselves); for child sessions fall back to
		// direct children.
		runs, err = api.q.ListDescendantRuns(r.Context(), id)
		if err == nil && len(runs) == 0 {
			runs, err = api.q.ListChildRuns(r.Context(), id)
		}
	} else {
		runs, err = api.q.ListChildRuns(r.Context(), id)
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

// DownloadRunLogs streams the complete log directory for an execution tree.
// A request for either the root run or a child run resolves to the same root,
// so nested sessions are always included in one archive.
func (api *API) DownloadRunLogs(w http.ResponseWriter, r *http.Request) {
	run := api.runFromCtx(r) // loaded + authorized by LoadRun
	rootRun, err := api.rootRun(r, run)
	if err != nil {
		api.respondError(w, http.StatusNotFound, "parent run not found")
		return
	}

	logDir := filesystem.NewPaths(LoadSettings().BasePath).RunLogsDir(rootRun.Task.Company.ShortName, rootRun.TaskID, rootRun.ID)
	if _, err := os.Stat(logDir); err != nil {
		if os.IsNotExist(err) {
			api.respondError(w, http.StatusNotFound, "run logs not found")
		} else {
			api.respondError(w, http.StatusInternalServerError, "failed to access run logs")
		}
		return
	}
	var files []string
	if err := filepath.WalkDir(logDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to enumerate run logs")
		return
	}
	if len(files) == 0 {
		api.respondError(w, http.StatusNotFound, "run logs not found")
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("run-%d-logs.zip", rootRun.ID)))
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, path := range files {
		rel, err := filepath.Rel(logDir, path)
		if err != nil {
			return
		}
		file, err := os.Open(path)
		if err != nil {
			return
		}
		writer, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			file.Close()
			return
		}
		_, _ = io.Copy(writer, file)
		file.Close()
	}
}

func (api *API) rootRun(r *http.Request, run db.Run) (db.Run, error) {
	if run.RootRunID != nil {
		return api.q.GetRun(r.Context(), *run.RootRunID)
	}
	seen := map[int32]bool{}
	for run.ParentRunID != nil {
		if seen[run.ID] {
			return db.Run{}, fmt.Errorf("run hierarchy cycle")
		}
		seen[run.ID] = true
		parent, err := api.q.GetRun(r.Context(), *run.ParentRunID)
		if err != nil {
			return db.Run{}, err
		}
		run = parent
	}
	return run, nil
}

func (api *API) RerunTask(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask

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
		var blocked *engine.TaskDependencyBlockedError
		if errors.As(err, &blocked) {
			api.respondJSON(w, http.StatusConflict, map[string]interface{}{"error": err.Error(), "task_id": blocked.TaskID, "blocked_by": blocked.Blockers})
			return
		}
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
		api.respondError(w, http.StatusNotFound, "run not found")
		return
	}
	if _, err := api.authorizeRun(r, run.ID); err != nil {
		api.respondError(w, http.StatusNotFound, "run not found")
		return
	}
	api.respondJSON(w, http.StatusOK, toRunResponse(run))
}

func (api *API) StopRun(w http.ResponseWriter, r *http.Request) {
	run := api.runFromCtx(r) // loaded + authorized by LoadRun

	if run.Status != "running" {
		api.respondError(w, http.StatusBadRequest, "Run is not in progress")
		return
	}

	api.engine.StopRun(r.Context(), run.ID)

	api.respondJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
}

// DownloadRunLog streams the run's execution log as a JSONL attachment.
// Prefers the on-disk log file when present; otherwise serializes log_entries
// (or falls back to legacy log_content).
func (api *API) DownloadRunLog(w http.ResponseWriter, r *http.Request) {
	run := api.runFromCtx(r) // loaded + authorized by LoadRun

	filename := fmt.Sprintf("run-%d_run_log.jsonl", run.ID)
	if name := strings.TrimSpace(run.Name); name != "" {
		safe := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				return r
			}
			return -1
		}, name)
		safe = strings.Trim(safe, "-")
		if safe != "" {
			filename = safe + "_run_log.jsonl"
		}
	}

	if run.LogFilePath != "" {
		if info, statErr := os.Stat(run.LogFilePath); statErr == nil && !info.IsDir() {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
			http.ServeFile(w, r, run.LogFilePath)
			return
		}
	}

	var body []byte
	if run.LogEntries != "" && run.LogEntries != "[]" {
		var entries []json.RawMessage
		if json.Unmarshal([]byte(run.LogEntries), &entries) == nil && len(entries) > 0 {
			var b strings.Builder
			for _, entry := range entries {
				b.Write(entry)
				b.WriteByte('\n')
			}
			body = []byte(b.String())
		}
	}
	if len(body) == 0 {
		if run.LogContent == "" {
			api.respondError(w, http.StatusNotFound, "run has no log")
			return
		}
		body = []byte(run.LogContent)
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Write(body)
}
