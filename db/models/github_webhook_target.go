package models

import "time"

type GitHubWebhookTarget struct {
	ID                 int32      `json:"id" gorm:"primaryKey"`
	DeliveryID         string     `json:"delivery_id" gorm:"not null;uniqueIndex:idx_github_delivery_task"`
	TaskID             int32      `json:"task_id" gorm:"not null;uniqueIndex:idx_github_delivery_task"`
	CommentID          int32      `json:"comment_id" gorm:"not null"`
	WakeStatus         string     `json:"wake_status" gorm:"not null;default:'pending'"`
	WakeAttemptToken   string     `json:"-" gorm:"index"`
	WakeLeaseExpiresAt *time.Time `json:"-"`
	WakeAttempts       int        `json:"wake_attempts" gorm:"not null;default:0"`
	WakeLastError      string     `json:"wake_last_error" gorm:"type:text"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// A committed comment is an outbox item until the matching agent wake has
// succeeded. This makes a failed engine invocation retryable without ever
// creating the comment twice.
