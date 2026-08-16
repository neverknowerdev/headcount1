package models

import "time"

type GitHubOAuthState struct {
	ID           string     `json:"-" gorm:"primaryKey"`
	RedirectURL  string     `json:"-"`
	MCPServerID  int32      `json:"-" gorm:"default:0"`
	UserID       int32      `json:"-" gorm:"default:0"`
	MCPAccountID int32      `json:"-" gorm:"default:0"`
	ReturnPath   string     `json:"-"`
	ExpiresAt    time.Time  `json:"-"`
	UsedAt       *time.Time `json:"-"`
	CreatedAt    time.Time
}

// MCPAccount details bind an OAuth callback to the account the user chose
// from the integrations screen.  This keeps personal and work identities
// separate even when they use the same GitHub App installation.
