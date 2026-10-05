package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type GitHubIdentityRepository struct{ db *gorm.DB }

func NewGitHubIdentityRepository(db *gorm.DB) *GitHubIdentityRepository {
	return &GitHubIdentityRepository{db: db}
}
func (q *GitHubIdentityRepository) HasGitHubIdentity(ctx context.Context, account MCPAccount, userID int32) (bool, error) {
	if account.UserID == nil || *account.UserID != userID {
		return false, nil
	}
	var count int64
	err := q.db.WithContext(ctx).Model(&GitHubIdentity{}).Where("mcp_account_id = ? AND mcp_server_id = ? AND user_id = ?", account.ID, account.MCPServerID, userID).Count(&count).Error
	return count == 1, err
}
