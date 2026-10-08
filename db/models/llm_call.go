package models

import "time"

// Model tiers a call is billed to.
const (
	TierSmart      = "smart"
	TierCheap      = "cheap"
	TierClassifier = "classifier"
	TierCommit     = "commit"
)

// Outcome of one model call.
const (
	LLMCallOK       = "ok"
	LLMCallError    = "error"
	LLMCallRejected = "rejected"
)

// LLMCall is one row of the usage ledger: a single model call of any kind,
// including failed and rejected ones, since each costs tokens and time. It is
// the only source for usage statistics. StepID links a smart call to its
// journal step; RunID with LogSeq locates an executor turn in its session log.
type LLMCall struct {
	ID         int64  `json:"id" gorm:"primaryKey"`
	CompanyID  int32  `json:"company_id" gorm:"not null"`
	RootTaskID *int32 `json:"root_task_id,omitempty"`
	TaskID     *int32 `json:"task_id,omitempty"`
	AgentID    *int32 `json:"agent_id,omitempty"`
	AgentName  string `json:"agent_name" gorm:"not null;default:''"`
	Tier       string `json:"tier" gorm:"not null"`
	// Purpose says what the call was for within its tier, e.g. "step",
	// "executor_turn", "compress", "checkpoint_gate".
	Purpose string `json:"purpose" gorm:"not null;default:''"`
	// Phase is the phase of the task that made the call; WorkflowPhase the
	// phase of the top-level task the work was spawned under.
	Phase         string `json:"phase" gorm:"not null;default:''"`
	WorkflowPhase string `json:"workflow_phase" gorm:"not null;default:''"`
	ProviderID    *int32 `json:"provider_id,omitempty"`
	ProviderName  string `json:"provider_name" gorm:"not null;default:''"`
	// Model is the model that answered; RequestedModel what was asked for,
	// which differs when a model group routed the call.
	Model            string    `json:"model" gorm:"not null;default:''"`
	RequestedModel   string    `json:"requested_model" gorm:"not null;default:''"`
	StepID           *int64    `json:"step_id,omitempty"`
	RunID            *int32    `json:"run_id,omitempty"`
	LogSeq           *int64    `json:"log_seq,omitempty"`
	PromptTokens     int       `json:"prompt_tokens" gorm:"not null;default:0"`
	CompletionTokens int       `json:"completion_tokens" gorm:"not null;default:0"`
	ReasoningTokens  int       `json:"reasoning_tokens" gorm:"not null;default:0"`
	CachedTokens     int       `json:"cached_tokens" gorm:"not null;default:0"`
	DurationMs       int64     `json:"duration_ms" gorm:"not null;default:0"`
	Status           string    `json:"status" gorm:"not null;default:'ok'"`
	Error            string    `json:"error" gorm:"not null;default:''"`
	CreatedAt        time.Time `json:"created_at"`
}

func (LLMCall) TableName() string { return "llm_calls" }
