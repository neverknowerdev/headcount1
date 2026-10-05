package models

import "time"

type ModelGroupMember struct {
	ID         int32       `json:"id" gorm:"primaryKey"`
	GroupID    int32       `json:"group_id" gorm:"not null;index"`
	ProviderID int32       `json:"provider_id" gorm:"not null"`
	Provider   LLMProvider `json:"provider" gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE;"`
	Model      string      `json:"model"`
	AllModels  bool        `json:"all_models" gorm:"not null;default:false"`
	IsFree     bool        `json:"is_free" gorm:"not null;default:false"`
	Priority   int         `json:"priority" gorm:"not null;default:0"`
	CreatedAt  time.Time   `json:"created_at"`
}

// Model is the concrete model id to route to. Empty when AllModels is
// set — the member then stands for every model currently listed in the
// provider's SupportedModels, resolved at request time (so it tracks a
// provider's catalog automatically as it changes).
