package models

import "time"

type ModelGroup struct {
	ID          int32              `json:"id" gorm:"primaryKey"`
	Name        string             `json:"name" gorm:"not null"`
	Slug        string             `json:"slug" gorm:"not null;uniqueIndex"`
	UserID      *int32             `json:"user_id" gorm:"index"`
	Description string             `json:"description"`
	Members     []ModelGroupMember `json:"members" gorm:"foreignKey:GroupID"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
