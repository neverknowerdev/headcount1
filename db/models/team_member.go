package models

import "time"

type TeamMember struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	TeamID    int32     `json:"team_id" gorm:"not null;uniqueIndex:idx_team_member"`
	Team      Team      `json:"-" gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE;"`
	UserID    int32     `json:"user_id" gorm:"not null;uniqueIndex:idx_team_member"`
	User      User      `json:"-" gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE;"`
	Role      string    `json:"role" gorm:"not null;default:'member'"`
	CreatedAt time.Time `json:"created_at"`
}
