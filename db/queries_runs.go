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

func (q *Queries) UpdateRunLog(ctx context.Context, id int32, content string, status string) error {
	var r Run
	err := q.db.WithContext(ctx).First(&r, id).Error
	if err != nil {
		return err
	}
	r.LogContent = content
	r.Status = status
	if status == "completed" || status == "failed" {
		now := gorm.Expr("CURRENT_TIMESTAMP")
		return q.db.WithContext(ctx).Model(&r).Updates(map[string]interface{}{"log_content": content, "status": status, "ended_at": now}).Error
	}
	return q.db.WithContext(ctx).Save(&r).Error
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
		// Find the first "request" entry — that's always the engine's
		// OpenCode message (the proxy's LLM request logs come after).
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
	err := q.db.WithContext(ctx).First(&r, id).Error
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
	if delta == (RunTokenStats{}) {
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
		cur.TotalTokens = cur.PromptTokens + cur.CompletionTokens + cur.ReasoningTokens + cur.ToolInputTokens + cur.ToolOutputTokens
		b, _ := json.Marshal(cur)
		return tx.Model(&Run{}).Where("id = ?", runID).Update("token_stats", string(b)).Error
	})
}
