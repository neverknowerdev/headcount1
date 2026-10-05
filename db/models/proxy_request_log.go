package models

import "time"

type ProxyRequestLog struct {
	ID               int32       `json:"id" gorm:"primaryKey"`
	AgentID          int32       `json:"agent_id" gorm:"not null"`
	Agent            Agent       `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:CASCADE;"`
	ProviderID       int32       `json:"provider_id" gorm:"not null"`
	Provider         LLMProvider `json:"provider" gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE;"`
	Model            string      `json:"model" gorm:"not null"`
	PromptTokens     int         `json:"prompt_tokens"`
	CompletionTokens int         `json:"completion_tokens"`
	TotalTokens      int         `json:"total_tokens"`
	CreatedAt        time.Time   `json:"created_at"`
}
