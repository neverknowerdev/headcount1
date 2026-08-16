package models

import "time"

type UserGitCredential struct {
	ID                     int32     `json:"id" gorm:"primaryKey"`
	UserID                 *int32    `json:"user_id" gorm:"uniqueIndex;not null"`
	User                   *User     `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	SSHPrivateKeyEncrypted string    `json:"-" gorm:"column:ssh_private_key;type:text;serializer:sealed"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// SEALED PEM; decrypt at use via secrets.Default().Decrypt()
