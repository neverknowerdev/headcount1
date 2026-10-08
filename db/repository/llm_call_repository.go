package repository

import (
	"context"
	"fmt"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

// LLMCallRepository is the usage ledger: it records every model call and
// answers every usage question from those rows alone.
type LLMCallRepository struct{ db *gorm.DB }

func NewLLMCallRepository(db *gorm.DB) *LLMCallRepository {
	return &LLMCallRepository{db: db}
}

func (q *LLMCallRepository) RecordLLMCall(ctx context.Context, call LLMCall) (LLMCall, error) {
	err := q.db.WithContext(ctx).Create(&call).Error
	return call, err
}

func (q *LLMCallRepository) GetLLMCall(ctx context.Context, id int64) (LLMCall, error) {
	var call LLMCall
	err := q.db.WithContext(ctx).First(&call, id).Error
	return call, err
}

// UsageFilter narrows the ledger. CompanyID is always required: usage never
// crosses a tenant. Zero-valued fields do not filter.
type UsageFilter struct {
	CompanyID int32
	// TaskID matches calls made by exactly that task; SubtreeOf matches the
	// task and everything beneath it.
	TaskID        *int32
	SubtreeOf     *int32
	AgentID       *int32
	Model         string
	Tier          string
	WorkflowPhase string
	From          *time.Time
	To            *time.Time
	// FailedOnly keeps the calls that did not produce a usable answer.
	FailedOnly bool
}

// UsageTotals aggregates a set of calls.
type UsageTotals struct {
	Calls            int64 `json:"calls"`
	FailedCalls      int64 `json:"failed_calls"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	DurationMs       int64 `json:"duration_ms"`
}

// UsageGroup is the totals of one value of a dimension. Key identifies the
// value (a task ID, an agent ID, a model name, ...) and Label is its display
// text.
type UsageGroup struct {
	Key   string `json:"key" gorm:"column:group_key"`
	Label string `json:"label" gorm:"column:group_label"`
	UsageTotals
}

// Dimensions usage can be grouped by.
const (
	UsageByTask  = "task"
	UsageByPhase = "phase"
	UsageByAgent = "agent"
	UsageByModel = "model"
	UsageByTier  = "tier"
	// UsageByPhaseTier splits each workflow phase into its smart and cheap
	// spend; its keys read "phase/tier".
	UsageByPhaseTier = "phase_tier"
	UsageByDay       = "day"
	UsageByRoot      = "root_task"
	usageTotalSQL    = `COUNT(*) AS calls,
COALESCE(SUM(CASE WHEN llm_calls.status <> 'ok' THEN 1 ELSE 0 END), 0) AS failed_calls,
COALESCE(SUM(llm_calls.prompt_tokens), 0) AS prompt_tokens,
COALESCE(SUM(llm_calls.completion_tokens), 0) AS completion_tokens,
COALESCE(SUM(llm_calls.reasoning_tokens), 0) AS reasoning_tokens,
COALESCE(SUM(llm_calls.cached_tokens), 0) AS cached_tokens,
COALESCE(SUM(llm_calls.duration_ms), 0) AS duration_ms`
)

func (q *LLMCallRepository) filtered(ctx context.Context, f UsageFilter) *gorm.DB {
	query := q.db.WithContext(ctx).Table("llm_calls").Where("llm_calls.company_id = ?", f.CompanyID)
	if f.TaskID != nil {
		query = query.Where("llm_calls.task_id = ?", *f.TaskID)
	}
	if f.SubtreeOf != nil {
		query = query.Where(`llm_calls.task_id IN (
WITH RECURSIVE subtree(id) AS (
  SELECT id FROM tasks WHERE id = ?
  UNION ALL
  SELECT child.id FROM tasks AS child JOIN subtree ON child.parent_id = subtree.id
) SELECT id FROM subtree)`, *f.SubtreeOf)
	}
	if f.AgentID != nil {
		query = query.Where("llm_calls.agent_id = ?", *f.AgentID)
	}
	if f.Model != "" {
		query = query.Where("llm_calls.model = ?", f.Model)
	}
	if f.Tier != "" {
		query = query.Where("llm_calls.tier = ?", f.Tier)
	}
	if f.WorkflowPhase != "" {
		query = query.Where("llm_calls.workflow_phase = ?", f.WorkflowPhase)
	}
	if f.From != nil {
		query = query.Where("llm_calls.created_at >= ?", *f.From)
	}
	if f.To != nil {
		query = query.Where("llm_calls.created_at < ?", *f.To)
	}
	if f.FailedOnly {
		query = query.Where("llm_calls.status <> ?", LLMCallOK)
	}
	return query
}

// UsageTotals sums the calls matching the filter.
func (q *LLMCallRepository) UsageTotals(ctx context.Context, f UsageFilter) (UsageTotals, error) {
	var totals UsageTotals
	err := q.filtered(ctx, f).Select(usageTotalSQL).Scan(&totals).Error
	return totals, err
}

// UsageBy sums the calls matching the filter per value of one dimension,
// largest first.
func (q *LLMCallRepository) UsageBy(ctx context.Context, f UsageFilter, dimension string) ([]UsageGroup, error) {
	query := q.filtered(ctx, f)
	var key, label string
	switch dimension {
	case UsageByTask:
		query = query.Joins("LEFT JOIN tasks ON tasks.id = llm_calls.task_id")
		key = "COALESCE(CAST(llm_calls.task_id AS TEXT), '')"
		label = "COALESCE(MAX(NULLIF(tasks.ref_key, '') || ' ' || tasks.title), MAX(tasks.title), '')"
	case UsageByRoot:
		query = query.Joins("LEFT JOIN tasks ON tasks.id = llm_calls.root_task_id")
		key = "COALESCE(CAST(llm_calls.root_task_id AS TEXT), '')"
		label = "COALESCE(MAX(NULLIF(tasks.ref_key, '') || ' ' || tasks.title), MAX(tasks.title), '')"
	case UsageByPhase:
		key = "llm_calls.workflow_phase"
		label = "MAX(llm_calls.workflow_phase)"
	case UsageByAgent:
		key = "COALESCE(CAST(llm_calls.agent_id AS TEXT), '')"
		label = "MAX(llm_calls.agent_name)"
	case UsageByModel:
		key = "llm_calls.model"
		label = "MAX(llm_calls.model)"
	case UsageByTier:
		key = "llm_calls.tier"
		label = "MAX(llm_calls.tier)"
	case UsageByPhaseTier:
		key = "llm_calls.workflow_phase || '/' || llm_calls.tier"
		label = "MAX(llm_calls.workflow_phase || '/' || llm_calls.tier)"
	case UsageByDay:
		// The stored timestamp's leading ten characters are its calendar date
		// in both SQLite and PostgreSQL.
		key = "substr(CAST(llm_calls.created_at AS TEXT), 1, 10)"
		label = "MAX(substr(CAST(llm_calls.created_at AS TEXT), 1, 10))"
	default:
		return nil, fmt.Errorf("unknown usage dimension %q", dimension)
	}
	var groups []UsageGroup
	err := query.
		Select(key + " AS group_key, " + label + " AS group_label, " + usageTotalSQL).
		Group(key).
		Order("SUM(llm_calls.prompt_tokens) + SUM(llm_calls.completion_tokens) DESC, 1").
		Scan(&groups).Error
	return groups, err
}

// ListLLMCalls returns the individual calls behind a filter, newest first.
// beforeID pages backwards: zero starts at the newest call.
func (q *LLMCallRepository) ListLLMCalls(ctx context.Context, f UsageFilter, beforeID int64, limit int) ([]LLMCall, error) {
	query := q.filtered(ctx, f)
	if beforeID > 0 {
		query = query.Where("llm_calls.id < ?", beforeID)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var calls []LLMCall
	err := query.Select("llm_calls.*").Order("llm_calls.id DESC").Limit(limit).Scan(&calls).Error
	return calls, err
}

// PreviousRunCallLogSeq returns the log position of the model call before a
// given position in an executor session's log, or zero if it is the first.
// What the log holds after that position, up to the given call's own answer
// and tool results, is that call's turn.
func (q *LLMCallRepository) PreviousRunCallLogSeq(ctx context.Context, runID int32, logSeq int64) (int64, error) {
	var previous int64
	err := q.db.WithContext(ctx).Table("llm_calls").
		Where("run_id = ? AND log_seq IS NOT NULL AND log_seq < ?", runID, logSeq).
		Select("COALESCE(MAX(log_seq), 0)").Scan(&previous).Error
	return previous, err
}
