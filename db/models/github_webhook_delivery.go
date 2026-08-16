package models

import "time"

type GitHubWebhookDelivery struct {
	ID             int32      `json:"id" gorm:"primaryKey"`
	DeliveryID     string     `json:"delivery_id" gorm:"uniqueIndex"`
	Event          string     `json:"event"`
	Status         string     `json:"status" gorm:"not null;default:'processing'"`
	ForwardedAt    *time.Time `json:"forwarded_at"`
	CompletedAt    *time.Time `json:"completed_at"`
	LastError      string     `json:"last_error" gorm:"type:text"`
	AttemptToken   string     `json:"-" gorm:"index"`
	LeaseExpiresAt *time.Time `json:"-"`
	Attempts       int        `json:"attempts" gorm:"not null;default:0"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// processing | failed | completed
// AttemptToken and LeaseExpiresAt form a compare-and-swap lease. They keep
// a redelivery from completing or failing work claimed by a newer request.
