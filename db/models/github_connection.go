package models

import "time"

type GitHubConnection struct {
	ID             int32     `json:"id" gorm:"primaryKey"`
	InstallationID int64     `json:"installation_id" gorm:"index;uniqueIndex:idx_github_connection_account_installation"`
	MCPAccountID   int32     `json:"mcp_account_id" gorm:"index;uniqueIndex:idx_github_connection_account_installation"`
	UserID         int32     `json:"user_id" gorm:"index"`
	AccountLogin   string    `json:"account_login"`
	ConnectedAt    time.Time `json:"connected_at"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// One account may only record an installation once. The installation is
// intentionally not globally unique: personal and work MCP accounts may
// both be allowed to use the same organisation installation.
