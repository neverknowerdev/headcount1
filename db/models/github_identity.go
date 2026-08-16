package models

import "time"

type GitHubIdentity struct {
	ID           int32  `json:"id" gorm:"primaryKey"`
	MCPAccountID int32  `json:"mcp_account_id" gorm:"not null;uniqueIndex"`
	MCPServerID  int32  `json:"mcp_server_id" gorm:"not null;uniqueIndex:idx_github_identity"`
	UserID       int32  `json:"user_id" gorm:"not null;uniqueIndex:idx_github_identity"`
	GitHubUserID int64  `json:"github_user_id" gorm:"not null;uniqueIndex:idx_github_identity"`
	GitHubLogin  string `json:"github_login"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
