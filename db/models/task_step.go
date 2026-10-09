package models

import "time"

// Kinds of journal step.
const (
	StepWorkflowStarted = "workflow_started"
	StepPhaseEntered    = "phase_entered"
	StepSmartCall       = "smart_call"
	StepSmartError      = "smart_error"
	StepSubtaskFinished = "subtask_finished"
	StepHumanQuestion   = "human_question"
	StepHumanAnswer     = "human_answer"
	StepRunStarted      = "run_started"
	StepRunFinished     = "run_finished"
	StepCheckpoint      = "checkpoint"
	StepWaiting         = "waiting"
	StepResumed         = "resumed"
	StepReviewRound     = "review_round"
	StepStopped         = "stopped"
	StepRerun           = "rerun"
	StepFinished        = "finished"
	StepNote            = "note"
)

// TaskStep is one entry in a task's append-only journal: a smart model's
// prompt and answer, a phase change, a subtask or executor run it refers to, a
// human question or its answer. The journal is both the task's log and the
// durable record the workflow driver reads; token usage for a step lives in
// the usage ledger (LLMCall), linked by step ID.
type TaskStep struct {
	ID         int64  `json:"id" gorm:"primaryKey"`
	TaskID     int32  `json:"task_id" gorm:"not null"`
	RootTaskID int32  `json:"root_task_id" gorm:"not null;default:0"`
	Kind       string `json:"kind" gorm:"not null"`
	Phase      string `json:"phase" gorm:"not null;default:''"`
	AgentID    *int32 `json:"agent_id,omitempty"`
	RunID      *int32 `json:"run_id,omitempty"`
	RefTaskID  *int32 `json:"ref_task_id,omitempty"`
	CommentID  *int32 `json:"comment_id,omitempty"`
	ToolName   string `json:"tool_name" gorm:"not null;default:''"`
	ToolArgs   string `json:"tool_args" gorm:"not null;default:''"`
	// Prompt is the complete request sent for a smart call; Response the raw
	// provider reply. Result is what the step did or produced.
	Prompt    string    `json:"prompt" gorm:"not null;default:''"`
	Response  string    `json:"response" gorm:"not null;default:''"`
	Result    string    `json:"result" gorm:"not null;default:''"`
	Error     string    `json:"error" gorm:"not null;default:''"`
	CreatedAt time.Time `json:"created_at"`
}

func (TaskStep) TableName() string { return "task_steps" }
