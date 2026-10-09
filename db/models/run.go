package models

import "time"

// Run is one executor session: a cheap-tier chat loop working on one direct
// task. Its conversation lives in an append-only log file; this row holds the
// session's status, its result and the cursor a paused session resumes from.
type Run struct {
	ID                int32      `json:"id" gorm:"primaryKey"`
	TaskID            int32      `json:"task_id" gorm:"not null"`
	Task              Task       `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	AgentID           int32      `json:"agent_id" gorm:"not null"`
	Agent             Agent      `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:CASCADE;"`
	Name              string     `json:"name" gorm:"index"`
	Title             string     `json:"title" gorm:"type:text"`
	Status            string     `json:"status" gorm:"not null"`
	SessionID         string     `json:"session_id"`
	LogFilePath       string     `json:"log_file_path"`
	LogContent        string     `json:"log_content"`
	LogEntries        string     `json:"log_entries" gorm:"type:text"`
	TokenStats        string     `json:"token_stats" gorm:"type:text"`
	ResultDescription string     `json:"result_description" gorm:"type:text"`
	ResultExplanation string     `json:"result_explanation" gorm:"type:text"`
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at"`
	LastMessageTime   *time.Time `json:"last_message_time"`
	WorkspacePath     string     `json:"workspace_path"`
	// Report is the executor's structured finish_work payload (JSON). The
	// task's next workflow step reads it; the session never writes task state.
	Report string `json:"report" gorm:"not null;default:''"`
	// Attempt numbers this session among the retries of its task, from 1.
	Attempt  int         `json:"attempt" gorm:"not null;default:0"`
	Recovery RunRecovery `json:"-" gorm:"serializer:json;type:jsonb"`
}

// Name is the human-readable session key: its task, the role that ran it and
// the attempt, as in "HC1-12-3-CODER-1".
// Recovery is internal control-plane state. Conversation history remains
// exclusively in the append-only JSONL log; this JSONB document stores only
// the cursor, planned-pause metadata, and short-lived resume lease.
