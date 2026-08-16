package models

import "time"

type Company struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	Name        string    `json:"name" gorm:"not null"`
	ShortName   string    `json:"short_name" gorm:"not null"`
	Description string    `json:"description"`
	Color       string    `json:"color"`
	TeamID      *int32    `json:"team_id" gorm:"index"`
	UserID      *int32    `json:"user_id" gorm:"index"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TeamID is the owning team: everything scoped to a company — projects,
// agents, tasks, runs — is visible to every member of that team. UserID
// records the member who created the company (and whose per-user Default
// Models the engine resolves for its background LLM calls).
