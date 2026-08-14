package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const (
	RunStatusPaused            = "paused"
	RunStatusRecoverableFailed = "recoverable_failed"
	RunStatusStale             = "stale"
	RunStatusResuming          = "resuming"
	CheckpointVersion          = 1
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
	if status == "completed" || status == "failed" || status == "canceled" || status == RunStatusStale || status == RunStatusRecoverableFailed {
		now := gorm.Expr("CURRENT_TIMESTAMP")
		updateErr = q.db.WithContext(ctx).Model(&r).Updates(map[string]interface{}{"log_content": content, "status": status, "ended_at": now}).Error
	} else {
		updateErr = q.db.WithContext(ctx).Save(&r).Error
	}
	if updateErr != nil || r.ParentRunID == nil {
		return updateErr
	}
	return q.EnqueueRunEvent(ctx, RunEvent{TaskID: r.TaskID, RunID: r.ID, EventType: "run_status", Payload: status, DedupeKey: fmt.Sprintf("run:%d:status:%s", r.ID, status)})
}

// MarkRunStale atomically retires a run that has stopped heartbeating. The
// conditional update makes the minute monitor safe to run concurrently with
// normal completion or an explicit resume claim.
func (q *Queries) MarkRunStale(ctx context.Context, runID int32, reason string) (bool, error) {
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return false, err
	}
	if run.Status != "running" {
		return false, nil
	}
	run.Recovery.RecoveryReason = reason
	result := q.db.WithContext(ctx).Model(&Run{}).Where("id = ? AND status = ?", runID, "running").Updates(map[string]interface{}{
		"status": RunStatusStale, "ended_at": ptrTime(time.Now()), "recovery": recoveryJSON(run.Recovery),
	})
	return result.RowsAffected == 1, result.Error
}

func ptrTime(t time.Time) *time.Time { return &t }

// recoveryJSON mirrors GORM's serializer:json conversion for targeted
// Updates calls. Map updates bypass the model field serializer, so encode the
// compact recovery document explicitly while avoiding a full Run rewrite.
func recoveryJSON(recovery RunRecovery) string {
	payload, _ := json.Marshal(recovery)
	return string(payload)
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

func (q *Queries) GetOrchestratorRun(ctx context.Context, taskID int32) (Run, error) {
	var task Task
	if err := q.db.WithContext(ctx).First(&task, taskID).Error; err != nil {
		return Run{}, err
	}
	if task.OrchestratorRunID == nil {
		return Run{}, gorm.ErrRecordNotFound
	}
	return q.GetRun(ctx, *task.OrchestratorRunID)
}

func (q *Queries) ListOrchestratorSessions(ctx context.Context, orchestratorRunID int32) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).
		Preload("Agent").
		Where("root_run_id = ? AND id <> ?", orchestratorRunID, orchestratorRunID).
		Order("started_at asc").Find(&runs).Error
	return runs, err
}

func (q *Queries) ListOrchestratorsByTask(ctx context.Context, taskID int32) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).
		Joins("JOIN tasks ON tasks.orchestrator_run_id = runs.id").
		Where("tasks.id = ?", taskID).
		Order("runs.started_at asc").Find(&runs).Error
	return runs, err
}

func (q *Queries) ListWaitingOrchestrators(ctx context.Context) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).
		Where("parent_run_id IS NULL AND status IN ? AND id IN (?)", []string{"running", "waiting"},
			q.db.Model(&Task{}).Select("orchestrator_run_id").Where("orchestrator_run_id IS NOT NULL")).
		Find(&runs).Error
	return runs, err
}

func (q *Queries) SetRunWaitState(ctx context.Context, runID int32, reason string) error {
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return err
	}
	run.Status = "waiting"
	run.Recovery.WaitReason = reason
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Updates(map[string]interface{}{"status": run.Status, "recovery": recoveryJSON(run.Recovery)}).Error
}

func (q *Queries) SetRunStopCause(ctx context.Context, runID int32, cause string) error {
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return err
	}
	run.Recovery.StopCause = cause
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Update("recovery", recoveryJSON(run.Recovery)).Error
}

func (q *Queries) IncrementRunRecoveryAttempts(ctx context.Context, runID int32) error {
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return err
	}
	run.Recovery.RecoveryAttempts++
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Update("recovery", recoveryJSON(run.Recovery)).Error
}

func (q *Queries) EnqueueRunEvent(ctx context.Context, event RunEvent) error {
	if event.DedupeKey != "" {
		var existing RunEvent
		if err := q.db.WithContext(ctx).Where("dedupe_key = ?", event.DedupeKey).First(&existing).Error; err == nil {
			return nil
		}
	}
	return q.db.WithContext(ctx).Create(&event).Error
}

func (q *Queries) ListPendingRunEvents(ctx context.Context, taskID int32) ([]RunEvent, error) {
	var events []RunEvent
	err := q.db.WithContext(ctx).Where("task_id = ? AND consumed_at IS NULL", taskID).Order("created_at asc").Find(&events).Error
	return events, err
}

func (q *Queries) ConsumeRunEvents(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	return q.db.WithContext(ctx).Model(&RunEvent{}).Where("id IN ? AND consumed_at IS NULL", ids).Update("consumed_at", now).Error
}

// GetRunningRuns returns every run currently marked running. It is used by the
// in-process liveness monitor to detect database rows left behind after a
// goroutine disappeared before its normal status finalizer ran.
func (q *Queries) GetRunningRuns(ctx context.Context) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).Where("status = ?", "running").Find(&runs).Error
	return runs, err
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
	var root Run
	if err := q.db.WithContext(ctx).Preload("Task").First(&root, rootRunID).Error; err != nil {
		return 0, err
	}
	query := q.db.WithContext(ctx).Model(&Run{}).
		Where("root_run_id = ? AND parent_run_id IS NOT NULL AND id <= ? AND agent_id = ?", rootRunID, runID, agentID)
	// In the new hierarchy the first direct child is the worker root, not a
	// delegated session. Legacy roots have no task orchestrator pointer and
	// retain the old counting behavior.
	if root.Task.OrchestratorRunID != nil && *root.Task.OrchestratorRunID == rootRunID {
		query = query.Where("parent_run_id <> ?", rootRunID)
	}
	var count int64
	err := query.Count(&count).Error
	return count, err
}

// PauseRunWithMetadata persists a versioned recovery checkpoint at a safe
// boundary on the Run row. The JSONL file remains the history source of truth;
// Recovery is only a cursor and recovery-coordination state.
func (q *Queries) PauseRunWithMetadata(ctx context.Context, runID int32, sequence int64, reason, initiator, target, phase string) error {
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return err
	}
	recovery := RunRecovery{
		CheckpointSequence: sequence, CheckpointVersion: CheckpointVersion,
		CheckpointPhase: CheckpointPhase(phase), RecoveryReason: reason,
		RecoveryInitiator: initiator, RecoveryTarget: target,
		ResumeAttempts: run.Recovery.ResumeAttempts,
	}
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status": RunStatusPaused, "recovery": recoveryJSON(recovery),
	}).Error
}

// GetRunsByRecoveryStates returns runs in the requested recoverable states.
// A checkpoint cursor is optional: failed/stale sessions may have crashed
// before one was persisted, so the engine derives the cursor from JSONL when
// the run is claimed for resume.
func (q *Queries) GetRunsByRecoveryStates(ctx context.Context, states []string) ([]Run, error) {
	var runs []Run
	if len(states) == 0 {
		return runs, nil
	}
	err := q.db.WithContext(ctx).
		Where("runs.status IN ?", states).
		Order("runs.id asc").Find(&runs).Error
	return runs, err
}

// ClaimRunForResume atomically claims a run for one recovery attempt. The
// cursor may be zero when the run failed before a planned checkpoint; the
// caller derives and supplies it from the JSONL trajectory at claim time.
func (q *Queries) ClaimRunForResume(ctx context.Context, runID int32, owner string, cause, previousStatus string, lease time.Time, allowedStates []string, sequence int64) (bool, error) {
	if owner == "" || len(allowedStates) == 0 {
		return false, fmt.Errorf("resume claim requires owner and allowed states")
	}
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return false, err
	}
	allowed := false
	for _, state := range allowedStates {
		if run.Status == state {
			allowed = true
			break
		}
	}
	if !allowed || (run.Recovery.ResumeLeaseUntil != nil && run.Recovery.ResumeLeaseUntil.After(time.Now())) {
		return false, nil
	}
	if previousStatus == "" {
		previousStatus = run.Status
	}
	run.Recovery.CheckpointSequence = sequence
	run.Recovery.CheckpointVersion = CheckpointVersion
	run.Recovery.ResumeLeaseOwner = owner
	run.Recovery.ResumeLeaseUntil = ptrTime(lease)
	run.Recovery.ResumePreviousStatus = previousStatus
	run.Recovery.ResumeAttempts++
	run.Recovery.LastResumeError = ""
	run.Recovery.RecoveryReason = cause
	result := q.db.WithContext(ctx).Model(&Run{}).Where("id = ? AND status IN ?", runID, allowedStates).Updates(map[string]interface{}{
		"status": RunStatusResuming, "recovery": recoveryJSON(run.Recovery),
	})
	return result.RowsAffected == 1, result.Error
}

// ReclaimExpiredResumeLeases returns interrupted resume attempts to the state
// they had before claiming, preserving failed/stale policy instead of turning
// every startup crash into an automatically paused run.
func (q *Queries) ReclaimExpiredResumeLeases(ctx context.Context, now time.Time) error {
	var runs []Run
	if err := q.db.WithContext(ctx).Where("status = ?", RunStatusResuming).Find(&runs).Error; err != nil {
		return err
	}
	for _, run := range runs {
		if run.Recovery.ResumeLeaseUntil == nil || !run.Recovery.ResumeLeaseUntil.Before(now) {
			continue
		}
		status := run.Recovery.ResumePreviousStatus
		if status == "" {
			status = RunStatusPaused
		}
		run.Recovery.ResumeLeaseOwner = ""
		run.Recovery.ResumeLeaseUntil = nil
		run.Recovery.ResumePreviousStatus = ""
		if err := q.db.WithContext(ctx).Model(&Run{}).Where("id = ? AND status = ?", run.ID, RunStatusResuming).Updates(map[string]interface{}{
			"status": status, "recovery": recoveryJSON(run.Recovery),
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// MarkRunResumeStarted transitions a claimed run to running without deleting
// the checkpoint. It is cleared only when the resumed run reaches a terminal
// state, so a crash during handoff remains recoverable.
func (q *Queries) MarkRunResumeStarted(ctx context.Context, runID int32, owner string) error {
	now := time.Now()
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return err
	}
	if run.Status != RunStatusResuming || run.Recovery.ResumeLeaseOwner != owner {
		return fmt.Errorf("run %d resume claim is no longer active", runID)
	}
	run.Recovery.ResumeLeaseOwner = ""
	run.Recovery.ResumeLeaseUntil = nil
	result := q.db.WithContext(ctx).Model(&Run{}).Where("id = ? AND status = ?", runID, RunStatusResuming).Updates(map[string]interface{}{
		"status": "running", "last_message_time": &now, "ended_at": nil,
		"recovery": recoveryJSON(run.Recovery),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("run %d resume claim is no longer active", runID)
	}
	return nil
}

// UpdateRunRecoveryMetadata records operator/context details without changing
// the checkpoint or its lifecycle state.
func (q *Queries) UpdateRunRecoveryMetadata(ctx context.Context, runID int32, reason, initiator, target string) error {
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return err
	}
	run.Recovery.RecoveryReason = reason
	run.Recovery.RecoveryInitiator = initiator
	run.Recovery.RecoveryTarget = target
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Update("recovery", recoveryJSON(run.Recovery)).Error
}

// RecordResumeError leaves a claimed run recoverable and makes a failed
// reconstruction visible to the next explicit or automatic attempt.
func (q *Queries) RecordResumeError(ctx context.Context, runID int32, resumeErr, recoverableStatus string) error {
	if recoverableStatus == "" {
		recoverableStatus = RunStatusPaused
	}
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return err
	}
	run.Recovery.LastResumeError = resumeErr
	run.Recovery.ResumeLeaseOwner = ""
	run.Recovery.ResumeLeaseUntil = nil
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status": recoverableStatus, "recovery": recoveryJSON(run.Recovery),
	}).Error
}

// ClearRunCheckpoint removes the transient recovery cursor after a resumed
// run has reached a durable terminal state.
func (q *Queries) ClearRunCheckpoint(ctx context.Context, runID int32) error {
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return err
	}
	// Keep the attempt counter as durable audit metadata while clearing the
	// cursor, lease, and last transient error consumed by a successful resume.
	attempts := run.Recovery.ResumeAttempts
	run.Recovery = RunRecovery{ResumeAttempts: attempts}
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Update("recovery", recoveryJSON(run.Recovery)).Error
}

// MarkRunRecoverable records a failed or stale run with a valid checkpoint for
// future explicit recovery. Automatic startup policy deliberately excludes
// these states for now.
func (q *Queries) MarkRunRecoverable(ctx context.Context, runID int32, status string, sequence int64, reason string) error {
	if status != RunStatusRecoverableFailed && status != RunStatusStale {
		return fmt.Errorf("unsupported recoverable run status %q", status)
	}
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return err
	}
	run.Recovery.CheckpointSequence = sequence
	run.Recovery.CheckpointVersion = CheckpointVersion
	run.Recovery.CheckpointPhase = CheckpointPhaseAfterTools
	run.Recovery.RecoveryReason = reason
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status": status, "recovery": recoveryJSON(run.Recovery),
	}).Error
}

// ResumeRun is the legacy helper retained for compatibility with older
// callers. New code should claim first and call MarkRunResumeStarted.
func (q *Queries) ResumeRun(ctx context.Context, runID int32) error {
	now := time.Now()
	var run Run
	if err := q.db.WithContext(ctx).First(&run, runID).Error; err != nil {
		return err
	}
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status": "running", "last_message_time": &now,
		"recovery": recoveryJSON(RunRecovery{ResumeAttempts: run.Recovery.ResumeAttempts}),
	}).Error
}
