package models

import "time"

type WebAuthnCredential struct {
	ID           int32  `json:"id" gorm:"primaryKey"`
	UserID       int32  `json:"user_id" gorm:"index;not null"`
	User         User   `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	CredentialID []byte `json:"-" gorm:"uniqueIndex;not null"`
	PublicKey    []byte `json:"-" gorm:"not null"`

	SignCount  uint32 `json:"-"`
	Transports string `json:"transports" gorm:"type:text"`
	AAGUID     []byte `json:"-"`

	BackupEligible bool   `json:"-"`
	BackupState    bool   `json:"-"`
	Nickname       string `json:"nickname"`
	WrappedDEK     string `json:"-" gorm:"not null"`
	PRFSalt        []byte `json:"-" gorm:"not null"`

	LastUsedAt time.Time `json:"last_used_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
