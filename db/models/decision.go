package models

import "time"

// Kinds of recorded decision.
const (
	DecisionKindDecision   = "decision"
	DecisionKindAssumption = "assumption"
	DecisionKindDeadEnd    = "dead_end"
)

// Decision is something an agent decided, assumed, or tried and abandoned.
// Decisions form a tree: across tasks it follows the task hierarchy, and
// within a task ParentDecisionID. A later decision that replaces an earlier
// one names it in SupersedesID.
type Decision struct {
	ID               int64  `json:"id" gorm:"primaryKey"`
	TaskID           int32  `json:"task_id" gorm:"not null"`
	RootTaskID       int32  `json:"root_task_id" gorm:"not null;default:0"`
	ParentDecisionID *int64 `json:"parent_decision_id,omitempty"`
	// StepID is the smart step that made the decision; RunID and LogSeq
	// locate it in an executor session's log instead.
	StepID       *int64    `json:"step_id,omitempty"`
	RunID        *int32    `json:"run_id,omitempty"`
	LogSeq       *int64    `json:"log_seq,omitempty"`
	Phase        string    `json:"phase" gorm:"not null;default:''"`
	AgentID      *int32    `json:"agent_id,omitempty"`
	Kind         string    `json:"kind" gorm:"not null;default:'decision'"`
	Title        string    `json:"title" gorm:"not null"`
	Decision     string    `json:"decision" gorm:"not null;default:''"`
	Rationale    string    `json:"rationale" gorm:"not null;default:''"`
	Alternatives string    `json:"alternatives" gorm:"not null;default:''"`
	SupersedesID *int64    `json:"supersedes_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func (Decision) TableName() string { return "decisions" }
