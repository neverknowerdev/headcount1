package models

import "time"

type ActivityLog struct {
	ID         int32     `json:"id" gorm:"primaryKey"`
	CompanyID  int32     `json:"company_id" gorm:"not null"`
	Company    Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Action     string    `json:"action" gorm:"not null"`
	EntityID   int32     `json:"entity_id"`
	EntityType string    `json:"entity_type"`
	Details    string    `json:"details"`
	CreatedAt  time.Time `json:"created_at"`
}

// e.g., "task_created", "task_status_updated", "agent_run_started"
// JSON string with more context
