package models

import "time"

type MCPAccount struct {
	ID                 int32     `json:"id" gorm:"primaryKey"`
	MCPServerID        int32     `json:"mcp_server_id" gorm:"not null;index"`
	Name               string    `json:"name" gorm:"not null"`
	AuthTokenEncrypted string    `json:"-" gorm:"column:auth_token;type:text;serializer:sealed"`
	HasToken           bool      `json:"has_token" gorm:"-"`
	UserID             *int32    `json:"user_id" gorm:"index"`
	LastError          string    `json:"last_error" gorm:"type:text"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// user label: "Personal", "Work"
// owning user; their DEK seals AuthTokenEncrypted
