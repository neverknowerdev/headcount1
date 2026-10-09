package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"agent-orchestrator/db"

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
	// AgentName is the role the session ran as.
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
	resp := RunResponse{Run: run, AgentName: run.Agent.Name}
	if run.LogEntries != "" {
		_ = json.Unmarshal([]byte(run.LogEntries), &resp.parsedEntries)
	}
	if run.TokenStats != "" {
		_ = json.Unmarshal([]byte(run.TokenStats), &resp.parsedTokenStats)
	}
	return resp
}

// runTask is a task as a list of sessions shows it: enough to name it, say
// how it stands and place it under its parent.
type runTask struct {
	ID           int32  `json:"id"`
	ParentID     *int32 `json:"parent_id"`
	RootTaskID   int32  `json:"root_task_id"`
	RefKey       string `json:"ref_key"`
	Title        string `json:"title"`
	TaskType     string `json:"task_type"`
	Status       string `json:"status"`
	Phase        string `json:"phase"`
	WaitingOn    string `json:"waiting_on"`
	WaitDetail   string `json:"wait_detail"`
	ResultReason string `json:"result_reason"`
}

// runTree is a set of sessions together with the tasks they belong to and
// every task above those, so they can be shown as the tasks are arranged:
// a top-level task, its subtasks beneath it, each with its own sessions.
type runTree struct {
	Runs  []RunResponse `json:"runs"`
	Tasks []runTask     `json:"tasks"`
}

// withTasksAbove loads the tasks of some sessions and their ancestors. A
// managed task has no sessions of its own, so the tasks at the top of a tree
// are found only by walking up from the ones that do.
func (api *API) withTasksAbove(runs []RunResponse) (runTree, error) {
	tree := runTree{Runs: runs, Tasks: []runTask{}}
	loaded := map[int32]bool{}
	var wanted []int32
	for _, run := range runs {
		if !loaded[run.TaskID] {
			loaded[run.TaskID] = true
			wanted = append(wanted, run.TaskID)
		}
	}
	for len(wanted) > 0 {
		var batch []runTask
		if err := api.db.Model(&db.Task{}).Where("id IN ?", wanted).Find(&batch).Error; err != nil {
			return tree, err
		}
		tree.Tasks = append(tree.Tasks, batch...)
		wanted = wanted[:0]
		for _, task := range batch {
			if task.ParentID != nil && !loaded[*task.ParentID] {
				loaded[*task.ParentID] = true
				wanted = append(wanted, *task.ParentID)
			}
		}
	}
	return tree, nil
}

// respondRuns answers a list of sessions: as a plain list, or with tree=true
// together with the tasks they sit under.
func (api *API) respondRuns(w http.ResponseWriter, r *http.Request, runs []db.Run) {
	out := make([]RunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunResponse(run))
	}
	if r.URL.Query().Get("tree") != "true" {
		api.respondJSON(w, http.StatusOK, out)
		return
	}
	tree, err := api.withTasksAbove(out)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, tree)
}

// runOverview narrows a query to what a list of sessions shows.
// log_content/log_entries are full transcripts (megabytes for long sessions)
// and are only rendered on the Run Log Details page, so they are left out to
// keep lists fast as history grows; the Task and Agent preloads are trimmed
// to the handful of fields a list renders.
func runOverview(tx *gorm.DB) *gorm.DB {
	return tx.
		Omit("log_content", "log_entries").
		Preload("Task", func(tx *gorm.DB) *gorm.DB { return tx.Select("id", "ref_key", "title") }).
		Preload("Agent", func(tx *gorm.DB) *gorm.DB { return tx.Select("id", "name") })
}

// ListCompanyRuns lists a company's executor sessions, newest first.
// subtree_of narrows them to one task and everything beneath it.
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

	var taskIDs []int32
	if under := r.URL.Query().Get("subtree_of"); under != "" {
		topID, _ := strconv.Atoi(under)
		subtree, err := api.q.ListTaskSubtree(r.Context(), int32(topID))
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, task := range subtree {
			if task.CompanyID == int32(compID) {
				taskIDs = append(taskIDs, task.ID)
			}
		}
	} else {
		api.db.Table("tasks").Where("company_id = ?", compID).Pluck("id", &taskIDs)
	}
	if len(taskIDs) == 0 {
		api.respondRuns(w, r, nil)
		return
	}

	var runs []db.Run
	if err := runOverview(api.db).Where("task_id IN ?", taskIDs).Order("started_at desc").Find(&runs).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondRuns(w, r, runs)
}

func (api *API) GetRun(w http.ResponseWriter, r *http.Request) {
	run := api.runFromCtx(r) // loaded + authorized by LoadRun
	resp := toRunResponse(run)
	var maxID int64
	api.db.Model(&db.Run{}).Where("task_id = ?", run.TaskID).Select("MAX(id)").Scan(&maxID)
	resp.IsLatest = int64(run.ID) == maxID
	api.respondJSON(w, http.StatusOK, resp)
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
