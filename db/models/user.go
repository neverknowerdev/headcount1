package models

import "time"

type User struct {
	ID                int32      `json:"id" gorm:"primaryKey"`
	Email             string     `json:"email" gorm:"uniqueIndex;not null"`
	IsAdmin           bool       `json:"is_admin" gorm:"not null;default:false"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ReenrollTokenHash string     `json:"-"`
	ReenrollExpiresAt *time.Time `json:"-"`
}

// stored lowercased/trimmed
// ReenrollTokenHash / ReenrollExpiresAt bind the re-enrollment that follows a
// passkey recovery to the browser that performed it. Recovery crypto-shreds
// the account's credentials, leaving it momentarily credential-less; without
// this ticket anyone who knows the email could race in and enroll their own
// passkey onto the (data-bearing) account. Set by RecoverConfirm, verified by
// RegisterBegin, cleared on a successful RegisterFinish. Empty for accounts
// that were never recovered (e.g. an abandoned first registration).
