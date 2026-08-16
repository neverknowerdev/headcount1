package models

import "time"

type PasswordResetToken struct {
	ID        int32      `json:"id" gorm:"primaryKey"`
	TokenHash string     `json:"-" gorm:"uniqueIndex;not null"`
	UserID    int32      `json:"user_id" gorm:"not null;index"`
	User      User       `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	ExpiresAt time.Time  `json:"expires_at" gorm:"not null"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `json:"created_at"`
}
