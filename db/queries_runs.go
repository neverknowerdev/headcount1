package db

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

func (q *Queries) CreateRun(ctx context.Context, r Run) (Run, error) {
	err := q.db.WithContext(ctx).Create(&r).Error
	return r, err
}

// UpdateRunStatus sets a run's status and (for failed runs) its error
// message. Terminal statuses also stamp ended_at.
func (q *Queries) UpdateRunStatus(ctx context.Context, id int32, status string, errorMsg string) error {
	updates := map[string]interface{}{"status": status, "error_message": errorMsg}
	if status == "completed" || status == "failed" || status == "canceled" {
		updates["ended_at"] = gorm.Expr("CURRENT_TIMESTAMP")
	}
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Updates(updates).Error
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

// CreateRunLogEntry inserts one log-entry metadata row. Seq is assigned
// atomically inside the INSERT (MAX(seq)+1 for the run), so concurrent
// writers—including other processes sharing the SQLite file—never collide.
func (q *Queries) CreateRunLogEntry(ctx context.Context, e RunLogEntry) error {
	return q.db.WithContext(ctx).Exec(`
		INSERT INTO run_log_entries
			(run_id, seq, type, ts, preview, tool_name, model, agent_name,
			 status_code, prompt_tokens, input_tokens, output_tokens,
			 child_run_id, byte_offset, byte_len, log_file_path)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM run_log_entries WHERE run_id = ?),
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.RunID, e.RunID, e.Type, e.Ts, e.Preview, e.ToolName, e.Model, e.AgentName,
		e.StatusCode, e.PromptTokens, e.InputTokens, e.OutputTokens,
		e.ChildRunID, e.ByteOffset, e.ByteLen, e.LogFilePath,
	).Error
}

// ListRunLogEntries returns every log-entry metadata row of a run in order.
func (q *Queries) ListRunLogEntries(ctx context.Context, runID int32) ([]RunLogEntry, error) {
	var entries []RunLogEntry
	err := q.db.WithContext(ctx).Where("run_id = ?", runID).Order("seq asc").Find(&entries).Error
	return entries, err
}

// GetRunLogEntry returns one entry of a run by its per-run sequence number.
func (q *Queries) GetRunLogEntry(ctx context.Context, runID, seq int32) (RunLogEntry, error) {
	var e RunLogEntry
	err := q.db.WithContext(ctx).Where("run_id = ? AND seq = ?", runID, seq).First(&e).Error
	return e, err
}

// ListRecentToolCallEntries returns the last n tool_call entries of a run,
// most recent first. Used by the proxy to pair tool results with the tool
// calls that produced them.
func (q *Queries) ListRecentToolCallEntries(ctx context.Context, runID int32, n int) ([]RunLogEntry, error) {
	var entries []RunLogEntry
	err := q.db.WithContext(ctx).
		Where("run_id = ? AND type = ?", runID, "tool_call").
		Order("seq desc").Limit(n).Find(&entries).Error
	return entries, err
}

// SetFirstRequestPromptTokens injects the actual prompt_tokens from an LLM
// response into the run's first "request" entry. The engine logs the request
// BEFORE any LLM call, so the exact count is only known after the LLM
// responds. Only the metadata row is updated — the JSONL file stays
// append-only.
func (q *Queries) SetFirstRequestPromptTokens(ctx context.Context, runID int32, promptTokens int) error {
	return q.db.WithContext(ctx).Exec(`
		UPDATE run_log_entries SET prompt_tokens = ?
		WHERE id = (SELECT id FROM run_log_entries WHERE run_id = ? AND type = 'request' ORDER BY seq ASC LIMIT 1)`,
		promptTokens, runID,
	).Error
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
	err := q.db.WithContext(ctx).Preload("Task").Preload("Task.Company").First(&r, runID).Error
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

// CountRunsByNameKey counts runs on a task whose name is the given key or a
// numbered variant of it ("<key>" or "<key>-N"). Used to pick the next run
// name suffix.
func (q *Queries) CountRunsByNameKey(ctx context.Context, taskID int32, key string) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).Model(&Run{}).
		Where("task_id = ? AND (name = ? OR name LIKE ?)", taskID, key, key+"-%").
		Count(&count).Error
	return count, err
}
