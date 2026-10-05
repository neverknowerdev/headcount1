package models

import "time"

type WebAuthnSession struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	UserID    *int32    `json:"user_id" gorm:"index"`
	Purpose   string    `json:"purpose" gorm:"not null"`
	Data      string    `json:"-" gorm:"type:text;not null"`
	ExpiresAt time.Time `json:"expires_at" gorm:"not null;index"`
	CreatedAt time.Time `json:"created_at"`
}

// nil until the user is known (registration)
// serialized webauthn.SessionData
