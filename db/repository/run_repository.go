package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type RunRepository struct{ db *gorm.DB }

func NewRunRepository(db *gorm.DB) *RunRepository { return &RunRepository{db: db} }

func (q *RunRepository) ListRunsByTask(ctx context.Context, taskID int32) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).Preload("Agent").Where("task_id = ?", taskID).Order("started_at asc, id asc").Find(&runs).Error
	return runs, err
}

const (
	RunStatusPaused   = "paused"
	RunStatusResuming = "resuming"
	CheckpointVersion = 1
)

func (q *RunRepository) CreateRun(ctx context.Context, r Run) (Run, error) {
	err := q.db.WithContext(ctx).Create(&r).Error
	return r, err
}

// UpdateRunLog records how a session ended (or its current status) and the
// message that explains it. A terminal status also stamps the end time.
func (q *RunRepository) UpdateRunLog(ctx context.Context, id int32, content string, status string) error {
	updates := map[string]interface{}{"log_content": content, "status": status}
	switch status {
	case "completed", "failed", "canceled":
		updates["ended_at"] = gorm.Expr("CURRENT_TIMESTAMP")
	}
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Updates(updates).Error
}

func ptrTime(t time.Time) *time.Time { return &t }

// recoveryJSON mirrors GORM's serializer:json conversion for targeted
// Updates calls. Map updates bypass the model field serializer, so encode the
// compact recovery document explicitly while avoiding a full Run rewrite.
func recoveryJSON(recovery RunRecovery) string {
	payload, _ := json.Marshal(recovery)
	return string(payload)
}

func (q *RunRepository) UpdateRunWorkspacePath(ctx context.Context, id int32, workspacePath string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update("workspace_path", workspacePath).Error
}

func (q *RunRepository) UpdateRunLogFilePath(ctx context.Context, id int32, filePath string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update("log_file_path", filePath).Error
}

func (q *RunRepository) AppendRunLogEntry(ctx context.Context, id int32, entry map[string]interface{}) error {
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	// PostgreSQL sessions can have the agent logger and proxy logger append to
	// the same run concurrently. An application-side read/append/write loses
	// entries and holds a transaction while copying an ever-growing JSON blob.
	// Let PostgreSQL serialize the row update and append the object atomically;
	// keep the stored value as text and avoid reparsing the entire growing JSON
	// array on every log line. The existing transaction path remains the
	// portable SQLite implementation.
	if q.db.Dialector.Name() == "postgres" {
		result := q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update(
			"log_entries",
			gorm.Expr(`CASE
				WHEN NULLIF(btrim(log_entries), '') IS NULL OR btrim(log_entries) = '[]'
					THEN '[' || ? || ']'
				ELSE left(rtrim(log_entries), length(rtrim(log_entries)) - 1) || ',' || ? || ']'
			END`, string(entryJSON), string(entryJSON)),
		)
		return result.Error
	}
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
func (q *RunRepository) UpdateLastRequestEntryTokens(ctx context.Context, runID int32, promptTokens int) error {
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

func (q *RunRepository) TouchRunLastMessageTime(ctx context.Context, id int32) error {
	now := time.Now()
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update("last_message_time", now).Error
}

func (q *RunRepository) GetRun(ctx context.Context, id int32) (Run, error) {
	var r Run
	err := q.db.WithContext(ctx).Preload("Task").Preload("Task.Company").Preload("Agent").First(&r, id).Error
	return r, err
}

func (q *RunRepository) GetRunWithTask(ctx context.Context, runID int32) (Run, Task, error) {
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

func (q *RunRepository) GetStaleRunningRuns(ctx context.Context, threshold time.Duration) ([]Run, error) {
	cutoff := time.Now().Add(-threshold)
	var runs []Run
	err := q.db.WithContext(ctx).
		Where("status = ? AND ((last_message_time IS NULL AND started_at < ?) OR last_message_time < ?)", "running", cutoff, cutoff).
		Find(&runs).Error
	return runs, err
}

// AddRunTokenStats atomically adds deltas to the run's persisted token
// stats. The column is stored as JSON; we read-modify-write the struct and
// round the totals so re-reads stay stable. Safe to call concurrently.
func (q *RunRepository) AddRunTokenStats(ctx context.Context, runID int32, delta RunTokenStats) error {
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

// UpdateRunReport stores an executor's structured finish_work report on its
// session. The task's workflow step reads it from here; writing it the moment
// the executor reports means the result survives a crash before that step.
func (q *RunRepository) UpdateRunReport(ctx context.Context, runID int32, report, summary, details string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).
		Updates(map[string]interface{}{
			"report":             report,
			"result_description": summary,
			"result_explanation": details,
		}).Error
}

// PauseRunWithMetadata persists a versioned recovery checkpoint at a safe
// boundary on the Run row. The JSONL file remains the history source of truth;
// Recovery is only a cursor and recovery-coordination state.
func (q *RunRepository) PauseRunWithMetadata(ctx context.Context, runID int32, sequence int64, reason, initiator, target, phase string) error {
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
func (q *RunRepository) GetRunsByRecoveryStates(ctx context.Context, states []string) ([]Run, error) {
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
func (q *RunRepository) ClaimRunForResume(ctx context.Context, runID int32, owner string, cause, previousStatus string, lease time.Time, allowedStates []string, sequence int64) (bool, error) {
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
func (q *RunRepository) ReclaimExpiredResumeLeases(ctx context.Context, now time.Time) error {
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
func (q *RunRepository) MarkRunResumeStarted(ctx context.Context, runID int32, owner string) error {
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

// ClearRunCheckpoint removes the transient recovery cursor after a resumed
// run has reached a durable terminal state.
func (q *RunRepository) ClearRunCheckpoint(ctx context.Context, runID int32) error {
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
