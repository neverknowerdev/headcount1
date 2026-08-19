package models

import "time"

const (
	RunKindTaskOrchestrator = "task_orchestrator"
	RunKindAgentSession     = "agent_session"
	RunKindCEOConsultation  = "ceo_consultation"
	RunKindHelperWorker     = "helper_worker"
)

type Run struct {
	ID                   int32       `json:"id" gorm:"primaryKey"`
	TaskID               int32       `json:"task_id" gorm:"not null"`
	Task                 Task        `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	AgentID              int32       `json:"agent_id" gorm:"not null"`
	Agent                Agent       `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:CASCADE;"`
	Name                 string      `json:"name" gorm:"index"`
	Kind                 string      `json:"kind" gorm:"not null;default:'agent_session';index"`
	ParentRunID          *int32      `json:"parent_run_id" gorm:"index"`
	RootRunID            *int32      `json:"root_run_id" gorm:"index"`
	LatestReportedStatus string      `json:"latest_reported_status" gorm:"column:current_status;default:''"`
	Status               string      `json:"status" gorm:"not null"`
	SessionID            string      `json:"session_id"`
	LogFilePath          string      `json:"log_file_path"`
	LogContent           string      `json:"log_content"`
	LogEntries           string      `json:"log_entries" gorm:"type:text"`
	TokenStats           string      `json:"token_stats" gorm:"type:text"`
	ResultDescription    string      `json:"result_description" gorm:"type:text"`
	ResultExplanation    string      `json:"result_explanation" gorm:"type:text"`
	StartedAt            time.Time   `json:"started_at"`
	EndedAt              *time.Time  `json:"ended_at"`
	LastMessageTime      *time.Time  `json:"last_message_time"`
	WorkspacePath        string      `json:"workspace_path"`
	Recovery             RunRecovery `json:"-" gorm:"serializer:json;type:jsonb"`
}

// Name is the human-readable run key: "<root task>-<AGENTSHORT>-<main>",
// with delegated sessions adding "-<sub-session>". For example,
// "HC1-2-CEO-1" and "HC1-2-CTO-2-1". Set when the run starts.
// Recovery is internal control-plane state. Conversation history remains
// exclusively in the append-only JSONL log; this JSONB document stores only
// the cursor, planned-pause metadata, and short-lived resume lease.
