package models

import "time"

type Agent struct {
	ID             int32        `json:"id" gorm:"primaryKey"`
	CompanyID      int32        `json:"company_id" gorm:"not null"`
	Company        Company      `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name           string       `json:"name" gorm:"not null"`
	RoleKey        string       `json:"role_key" gorm:"index;default:''"`
	ShortName      string       `json:"short_name" gorm:"default:''"`
	Description    string       `json:"description"`
	SystemPrompt   string       `json:"system_prompt" gorm:"not null"`
	ProviderID     *int32       `json:"provider_id"`
	Provider       *LLMProvider `json:"provider" gorm:"foreignKey:ProviderID;constraint:OnDelete:SET NULL;"`
	ModelGroupID   *int32       `json:"model_group_id"`
	ModelGroup     *ModelGroup  `json:"model_group,omitempty" gorm:"foreignKey:ModelGroupID;constraint:OnDelete:SET NULL;"`
	Model          string       `json:"model"`
	Mode           string       `json:"mode" gorm:"not null;default:'primary'"`
	ChatType       string       `json:"chat_type" gorm:"not null;default:'message_history'"`
	ReasoningLevel string       `json:"reasoning_level" gorm:"default:''"`
	Subagents      string       `json:"subagents" gorm:"type:text;default:''"`
	AllowedMCPs    string       `json:"allowed_mcps" gorm:"type:text;default:''"`
	Permissions    string       `json:"permissions"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	Skills         []Skill      `json:"skills" gorm:"many2many:agent_skills;"`
}
