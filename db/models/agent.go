package models

import "time"

// Agent is a role: a name, a description, and the prompt that gives a model
// that role's voice. Which model speaks is decided by the task and its tier,
// not by the agent; every executor has the full tool set whatever its role.
type Agent struct {
	ID        int32   `json:"id" gorm:"primaryKey"`
	CompanyID int32   `json:"company_id" gorm:"not null"`
	Company   Company `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name      string  `json:"name" gorm:"not null"`
	// Builtin roles ship with the product and cannot be removed or disabled.
	Builtin     bool   `json:"builtin" gorm:"not null;default:false"`
	Enabled     bool   `json:"enabled" gorm:"not null;default:true"`
	RoleKey     string `json:"role_key" gorm:"index;default:''"`
	ShortName   string `json:"short_name" gorm:"default:''"`
	Description string `json:"description"`
	// SystemPrompt is the role line plus whatever the user added to it.
	SystemPrompt string    `json:"system_prompt" gorm:"not null"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Skills       []Skill   `json:"skills" gorm:"many2many:agent_skills;"`
}
