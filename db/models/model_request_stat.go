package models

import "time"

type ModelRequestStat struct {
	ID               int32      `json:"id" gorm:"primaryKey"`
	GroupID          *int32     `json:"group_id" gorm:"index"`
	ProviderID       int32      `json:"provider_id" gorm:"not null;index"`
	Model            string     `json:"model" gorm:"not null;index"`
	Success          bool       `json:"success" gorm:"not null;default:false"`
	RateLimited      bool       `json:"rate_limited" gorm:"not null;default:false"`
	StatusCode       int        `json:"status_code"`
	DurationMs       int64      `json:"duration_ms"`
	PromptTokens     int        `json:"prompt_tokens"`
	CompletionTokens int        `json:"completion_tokens"`
	TokensPerSec     float64    `json:"tokens_per_sec"`
	CooldownUntil    *time.Time `json:"cooldown_until"`
	ErrorMessage     string     `json:"error_message" gorm:"type:text"`
	CreatedAt        time.Time  `json:"created_at" gorm:"index"`
}

// CooldownUntil is set on rate-limited rows: the gateway won't route to
// this provider+model again until this time (unless nothing else works).
