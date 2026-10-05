package models

import "time"

type Session struct {
	ID                int32     `json:"id" gorm:"primaryKey"`
	TokenHash         string    `json:"-" gorm:"uniqueIndex;not null"`
	UserID            int32     `json:"user_id" gorm:"not null;index"`
	User              User      `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	ExpiresAt         time.Time `json:"expires_at" gorm:"not null"`
	AbsoluteExpiresAt time.Time `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// AbsoluteExpiresAt is the hard ceiling on this access session, carried from
// the refresh-token family it was minted under. The 1h ExpiresAt slides
// forward on activity, but it can never slide past this — so an access
// cookie used hourly still dies at the family's absolute cap instead of
// renewing forever. Zero means unbounded (legacy rows only).
