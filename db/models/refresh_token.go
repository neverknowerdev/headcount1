package models

import "time"

type RefreshToken struct {
	ID                int32      `json:"-" gorm:"primaryKey"`
	FamilyID          string     `json:"-" gorm:"index;not null"`
	TokenHash         string     `json:"-" gorm:"uniqueIndex;not null"`
	UserID            int32      `json:"-" gorm:"not null;index"`
	User              User       `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	ExpiresAt         time.Time  `json:"-" gorm:"not null"`
	AbsoluteExpiresAt time.Time  `json:"-" gorm:"not null"`
	UsedAt            *time.Time `json:"-"`
	RevokedAt         *time.Time `json:"-"`
	CreatedAt         time.Time  `json:"-"`
}

// hard cap for the whole family
