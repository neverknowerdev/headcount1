package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

func (q *Queries) CreateRun(ctx context.Context, r Run) (Run, error) {
	err := q.db.WithContext(ctx).Create(&r).Error
	return r, err
}

func (q *Queries) UpdateRunLog(ctx context.Context, id int32, content string, status string) error {
	var r Run
	err := q.db.WithContext(ctx).First(&r, id).Error
	if err != nil {
		return err
	}
	r.LogContent = content
	r.Status = status
	var updateErr error
	if status == "completed" || status == "failed" {
		now := gorm.Expr("CURRENT_TIMESTAMP")
		updateErr = q.db.WithContext(ctx).Model(&r).Updates(map[string]interface{}{"log_content": content, "status": status, "ended_at": now}).Error
	} else {
		updateErr = q.db.WithContext(ctx).Save(&r).Error
	}
	if updateErr != nil {
		return updateErr
	}
	if r.Kind != "orchestrator" {
		_ = q.EnqueueRunEvent(ctx, RunEvent{TaskID: r.TaskID, RunID: r.ID, EventType: "run_status", Payload: status, DedupeKey: fmt.Sprintf("run:%d:%s:%d", r.ID, status, time.Now().UnixNano())})
	}
	return nil
}

// SetRunRootID sets root_run_id after creation. Root runs point at themselves
// so all sessions of one execution tree share the same root id.
func (q *Queries) SetRunRootID(ctx context.Context, id int32, rootID int32) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update("root_run_id", rootID).Error
}

// ListChildRuns returns all runs whose parent_run_id equals parentRunID,
// ordered by start time ascending. Used by the Run Log UI to render nested
// delegation sessions.
func (q *Queries) ListChildRuns(ctx context.Context, parentRunID int32) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).
		Preload("Task").Preload("Agent").
		Where("parent_run_id = ?", parentRunID).
		Order("started_at asc").
		Find(&runs).Error
	return runs, err
}

// ListDescendantRuns returns every run in the given root run's session tree
// except the root itself (all sessions share the root's id in root_run_id),
// ordered by start time ascending. Used for whole-tree per-agent token stats.
func (q *Queries) ListDescendantRuns(ctx context.Context, rootRunID int32) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).
		Preload("Task").Preload("Agent").
		Where("root_run_id = ? AND id <> ?", rootRunID, rootRunID).
		Order("started_at asc").
		Find(&runs).Error
	return runs, err
}

// UpdateRunCurrentStatus stores the agent's self-reported progress line.
func (q *Queries) UpdateRunCurrentStatus(ctx context.Context, id int32, status string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update("current_status", status).Error
}

func (q *Queries) UpdateRunSession(ctx context.Context, id int32, sessionID string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update("session_id", sessionID).Error
}

func (q *Queries) UpdateRunLogFilePath(ctx context.Context, id int32, filePath string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update("log_file_path", filePath).Error
}

func (q *Queries) AppendRunLogEntry(ctx context.Context, id int32, entry map[string]interface{}) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r Run
		err := tx.First(&r, id).Error
		if err != nil {
			return err
		}

		var entries []map[string]interface{}
		if r.LogEntries != "" {
			json.Unmarshal([]byte(r.LogEntries), &entries)
		}

		entries = append(entries, entry)
		entriesJSON, _ := json.Marshal(entries)

		return tx.Model(&Run{}).Where("id = ?", id).Update("log_entries", string(entriesJSON)).Error
	})
}

// UpdateLastRequestEntryTokens injects the actual prompt_tokens from an LLM
// response into the engine's "request" entry. The engine logs the request
// BEFORE any LLM call, so the exact count is only known after the LLM
// responds. The engine's request is the FIRST "request" entry in the run
// (subsequent requests are LLM call logs from the proxy, which already
// have their own token counts).
func (q *Queries) UpdateLastRequestEntryTokens(ctx context.Context, runID int32, promptTokens int) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r Run
		err := tx.First(&r, runID).Error
		if err != nil {
			return err
		}
		var entries []map[string]interface{}
		if r.LogEntries == "" {
			return nil
		}
		if err := json.Unmarshal([]byte(r.LogEntries), &entries); err != nil {
			return err
		}
		// Find the first "request" entry — the proxy's LLM request logs come after.
		for i := 0; i < len(entries); i++ {
			if entries[i]["type"] == "request" {
				entries[i]["prompt_tokens"] = promptTokens
				delete(entries[i], "est_prompt_tokens")
				break
			}
		}
		entriesJSON, _ := json.Marshal(entries)
		return tx.Model(&Run{}).Where("id = ?", runID).Update("log_entries", string(entriesJSON)).Error
	})
}

func (q *Queries) TouchRunLastMessageTime(ctx context.Context, id int32) error {
	now := time.Now()
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update("last_message_time", now).Error
}

func (q *Queries) GetRun(ctx context.Context, id int32) (Run, error) {
	var r Run
	err := q.db.WithContext(ctx).Preload("Task").Preload("Task.Company").Preload("Agent").First(&r, id).Error
	return r, err
}

func (q *Queries) GetRunWithTask(ctx context.Context, runID int32) (Run, Task, error) {
	var r Run
	err := q.db.WithContext(ctx).
		Preload("Task").
		Preload("Task.Company").
		Preload("Agent").
		First(&r, runID).Error
	if err != nil {
		return Run{}, Task{}, err
	}
	return r, r.Task, nil
}

func (q *Queries) GetRunBySessionID(ctx context.Context, sessionID string) (Run, error) {
	var r Run
	err := q.db.WithContext(ctx).Where("session_id = ?", sessionID).First(&r).Error
	return r, err
}

func (q *Queries) GetRunningRunsByTaskID(ctx context.Context, taskID int32) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).Where("task_id = ? AND status = ?", taskID, "running").Find(&runs).Error
	return runs, err
}

// GetOrchestratorRun returns the sidecar for a worker root, if one exists.
func (q *Queries) GetOrchestratorRun(ctx context.Context, workerRunID int32) (Run, error) {
	var r Run
	err := q.db.WithContext(ctx).Preload("Task").Preload("Agent").
		Where("kind = ? AND supervised_run_id = ?", "orchestrator", workerRunID).First(&r).Error
	return r, err
}

// ListSupervisedRuns returns the worker tree monitored by an orchestrator.
// The orchestrator itself is never included.
func (q *Queries) ListSupervisedRuns(ctx context.Context, workerRootID int32) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).Preload("Task").Preload("Agent").
		Where("(kind = ? OR kind = '') AND (id = ? OR root_run_id = ?)", "worker", workerRootID, workerRootID).
		Order("started_at asc, id asc").Find(&runs).Error
	return runs, err
}

func (q *Queries) ListOrchestratorsByTask(ctx context.Context, taskID int32) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).Where("task_id = ? AND kind = ?", taskID, "orchestrator").Order("started_at desc").Find(&runs).Error
	return runs, err
}

func (q *Queries) ListWaitingOrchestrators(ctx context.Context) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).Preload("Task").Preload("Agent").
		Where("kind = ? AND status = ?", "orchestrator", "waiting").Order("started_at asc").Find(&runs).Error
	return runs, err
}

func (q *Queries) SetRunKind(ctx context.Context, runID int32, kind string, supervisedRunID *int32) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"kind": kind, "supervised_run_id": supervisedRunID,
	}).Error
}

func (q *Queries) SetRunWaitState(ctx context.Context, runID int32, reason string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status": "waiting", "wait_reason": reason,
	}).Error
}

func (q *Queries) SetRunStopCause(ctx context.Context, runID int32, cause string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Update("stop_cause", cause).Error
}

func (q *Queries) IncrementRunRecoveryAttempts(ctx context.Context, runID int32) (int, error) {
	if err := q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).
		UpdateColumn("recovery_attempts", gorm.Expr("recovery_attempts + ?", 1)).Error; err != nil {
		return 0, err
	}
	var run Run
	if err := q.db.WithContext(ctx).Select("recovery_attempts").First(&run, runID).Error; err != nil {
		return 0, err
	}
	return run.RecoveryAttempts, nil
}

// EnqueueRunEvent persists a worker lifecycle event. DedupeKey is optional;
// callers that retry delivery can use it to avoid duplicate wakeups.
func (q *Queries) EnqueueRunEvent(ctx context.Context, event RunEvent) error {
	if event.DedupeKey != "" {
		var count int64
		if err := q.db.WithContext(ctx).Model(&RunEvent{}).Where("task_id = ? AND dedupe_key = ?", event.TaskID, event.DedupeKey).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
	}
	return q.db.WithContext(ctx).Create(&event).Error
}

func (q *Queries) ListPendingRunEvents(ctx context.Context, taskID int32) ([]RunEvent, error) {
	var events []RunEvent
	err := q.db.WithContext(ctx).Where("task_id = ? AND consumed_at IS NULL", taskID).Order("id asc").Find(&events).Error
	return events, err
}

func (q *Queries) ConsumeRunEvents(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	return q.db.WithContext(ctx).Model(&RunEvent{}).Where("id IN ? AND consumed_at IS NULL", ids).Update("consumed_at", now).Error
}

func (q *Queries) GetStaleRunningRuns(ctx context.Context, threshold time.Duration) ([]Run, error) {
	cutoff := time.Now().Add(-threshold)
	var runs []Run
	err := q.db.WithContext(ctx).
		Where("status = ? AND ((last_message_time IS NULL AND started_at < ?) OR last_message_time < ?)", "running", cutoff, cutoff).
		Find(&runs).Error
	return runs, err
}

func (q *Queries) IsRunStale(ctx context.Context, runID int32, threshold time.Duration) (bool, error) {
	cutoff := time.Now().Add(-threshold)
	var count int64
	err := q.db.WithContext(ctx).
		Model(&Run{}).
		Where("id = ? AND status = ? AND ((last_message_time IS NULL AND started_at < ?) OR last_message_time < ?)", runID, "running", cutoff, cutoff).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetRunTokenStats loads the persisted TokenStats JSON for a run. Returns a
// zero-value RunTokenStats (not an error) if the column is empty or
// malformed, so callers can treat missing data as "no tokens recorded yet".
func (q *Queries) GetRunTokenStats(ctx context.Context, runID int32) (RunTokenStats, error) {
	var r Run
	err := q.db.WithContext(ctx).Select("token_stats").First(&r, runID).Error
	if err != nil {
		return RunTokenStats{}, err
	}
	if r.TokenStats == "" {
		return RunTokenStats{}, nil
	}
	var stats RunTokenStats
	if err := json.Unmarshal([]byte(r.TokenStats), &stats); err != nil {
		return RunTokenStats{}, nil
	}
	return stats, nil
}

// AddRunTokenStats atomically adds deltas to the run's persisted token
// stats. The column is stored as JSON; we read-modify-write the struct and
// round the totals so re-reads stay stable. Safe to call concurrently.
func (q *Queries) AddRunTokenStats(ctx context.Context, runID int32, delta RunTokenStats) error {
	if delta.IsEmpty() {
		return nil
	}
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r Run
		if err := tx.Select("token_stats").First(&r, runID).Error; err != nil {
			return err
		}
		cur := RunTokenStats{}
		if r.TokenStats != "" {
			_ = json.Unmarshal([]byte(r.TokenStats), &cur)
		}
		cur.PromptTokens += delta.PromptTokens
		cur.CompletionTokens += delta.CompletionTokens
		cur.ReasoningTokens += delta.ReasoningTokens
		cur.ToolInputTokens += delta.ToolInputTokens
		cur.ToolOutputTokens += delta.ToolOutputTokens
		cur.CachedTokens += delta.CachedTokens
		cur.MCPToolTokens += delta.MCPToolTokens
		if len(delta.MCPServerTokens) > 0 {
			if cur.MCPServerTokens == nil {
				cur.MCPServerTokens = make(map[string]int, len(delta.MCPServerTokens))
			}
			for k, v := range delta.MCPServerTokens {
				cur.MCPServerTokens[k] += v
			}
		}
		cur.TotalTokens = cur.PromptTokens + cur.CompletionTokens + cur.ReasoningTokens + cur.ToolInputTokens + cur.ToolOutputTokens
		b, _ := json.Marshal(cur)
		return tx.Model(&Run{}).Where("id = ?", runID).Update("token_stats", string(b)).Error
	})
}

// UpdateRunResult stores the short description and detailed explanation produced
// by the finish_task_execution tool call at the end of a run.
func (q *Queries) UpdateRunResult(ctx context.Context, runID int32, description, explanation string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).
		Updates(map[string]interface{}{
			"result_description": description,
			"result_explanation": explanation,
		}).Error
}

// ListCompletedRunsByTask returns all completed runs for a task that have a
// result_description set, ordered by start time ascending.
func (q *Queries) ListCompletedRunsByTask(ctx context.Context, taskID int32) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).
		Where("task_id = ? AND status = ? AND result_description != ''", taskID, "completed").
		Order("started_at asc").
		Find(&runs).Error
	return runs, err
}

// GetLatestRunByTask returns the most recently started run for a task.
func (q *Queries) GetLatestRunByTask(ctx context.Context, taskID int32) (Run, error) {
	var r Run
	err := q.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("started_at desc").
		First(&r).Error
	return r, err
}

// UpdateRunName stores the human-readable run key (e.g. "DEC-50-CEO").
func (q *Queries) UpdateRunName(ctx context.Context, id int32, name string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update("name", name).Error
}

// CountRootRunsThrough returns the ordinal of rootRunID among the root runs
// for taskID. Run ids are monotonically assigned, so this remains stable even
// when older runs have been renamed or have an empty legacy name.
func (q *Queries) CountRootRunsThrough(ctx context.Context, taskID, rootRunID int32) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).Model(&Run{}).
		Where("task_id = ? AND parent_run_id IS NULL AND id <= ?", taskID, rootRunID).
		Count(&count).Error
	return count, err
}

// CountSubsessionRunsThrough returns the ordinal of a delegated session among
// sessions for the same root run and agent. The current run is included, so
// callers can use the result directly as the suffix.
func (q *Queries) CountSubsessionRunsThrough(ctx context.Context, rootRunID, runID, agentID int32) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).Model(&Run{}).
		Where("root_run_id = ? AND parent_run_id IS NOT NULL AND id <= ? AND agent_id = ?", rootRunID, runID, agentID).
		Count(&count).Error
	return count, err
}

// PauseRun marks a run "interrupted" and stores its serialized conversation
// history so it can be resumed after a restart (see NativeEngine's
// BeginDrain/ResumeInterruptedRuns). The task lock (Task.RunID) is
// deliberately left in place — this does not call UnlockTaskRun — so no new
// run can start on the same task until this one is either resumed or
// explicitly failed.
func (q *Queries) PauseRun(ctx context.Context, runID int32, history string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).
		Updates(map[string]interface{}{"status": "interrupted", "paused_history": history}).Error
}

// GetInterruptedRuns returns every run left paused by a graceful shutdown,
// across all tenants — consumed once at boot to resume them.
func (q *Queries) GetInterruptedRuns(ctx context.Context) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).Where("status = ?", "interrupted").Find(&runs).Error
	return runs, err
}

// ResumeRun marks a previously-interrupted run "running" again and clears its
// persisted history now that the caller has loaded it back into memory. It
// also refreshes last_message_time: without this, IsRunStale would see the
// pre-pause heartbeat (possibly many minutes old, e.g. across a slow update)
// and could race a concurrent ProcessTask call into treating the resumed run
// as stale and failing it out from under the resume goroutine.
func (q *Queries) ResumeRun(ctx context.Context, runID int32) error {
	now := time.Now()
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).
		Updates(map[string]interface{}{"status": "running", "paused_history": "", "last_message_time": now}).Error
}
