package models

import "time"

const (
	TeamRoleOwner  = "owner"
	TeamRoleMember = "member"
)

type Team struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name" gorm:"not null"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
