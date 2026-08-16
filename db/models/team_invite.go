package models

import "time"

type TeamInvite struct {
	ID         int32      `json:"id" gorm:"primaryKey"`
	TeamID     int32      `json:"team_id" gorm:"not null;index"`
	Team       Team       `json:"-" gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE;"`
	Email      string     `json:"email" gorm:"not null"`
	Role       string     `json:"role" gorm:"not null;default:'member'"`
	TokenHash  string     `json:"-" gorm:"uniqueIndex;not null"`
	InvitedBy  int32      `json:"invited_by" gorm:"not null"`
	ExpiresAt  time.Time  `json:"expires_at" gorm:"not null"`
	AcceptedAt *time.Time `json:"accepted_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// normalized; informational + pre-fill
