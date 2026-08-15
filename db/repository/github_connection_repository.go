package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"fmt"
	"gorm.io/gorm"
)

type GitHubConnectionRepository struct{ db *gorm.DB }

func NewGitHubConnectionRepository(db *gorm.DB) *GitHubConnectionRepository {
	return &GitHubConnectionRepository{db: db}
}
func (q *GitHubConnectionRepository) ListGitHubConnectionsForUser(ctx context.Context, userID int32) ([]GitHubConnection, error) {
	var connections []GitHubConnection
	err := q.db.WithContext(ctx).Where("user_id = ?", userID).Order("id").Find(&connections).Error
	return connections, err
}
func (q *GitHubConnectionRepository) GetGitHubConnectionForAccount(ctx context.Context, accountID, userID int32) (GitHubConnection, error) {
	var connection GitHubConnection
	err := q.db.WithContext(ctx).Where("mcp_account_id = ? AND user_id = ?", accountID, userID).Order("connected_at desc, id desc").First(&connection).Error
	return connection, err
}
func (q *GitHubConnectionRepository) ListGitHubConnectionsForAccount(ctx context.Context, accountID, userID int32) ([]GitHubConnection, error) {
	var connections []GitHubConnection
	err := q.db.WithContext(ctx).Where("mcp_account_id = ? AND user_id = ?", accountID, userID).Order("connected_at desc, id desc").Find(&connections).Error
	return connections, err
}
func (q *GitHubConnectionRepository) GetGitHubConnectionForAccountInstallation(ctx context.Context, accountID, userID int32, installationID int64) (GitHubConnection, error) {
	var connection GitHubConnection
	err := q.db.WithContext(ctx).Where("mcp_account_id = ? AND user_id = ? AND installation_id = ?", accountID, userID, installationID).First(&connection).Error
	return connection, err
}
func (q *GitHubConnectionRepository) UpsertGitHubConnection(ctx context.Context, connection GitHubConnection) error {
	return q.db.WithContext(ctx).Where("mcp_account_id = ? AND installation_id = ?", connection.MCPAccountID, connection.InstallationID).Assign(connection).FirstOrCreate(&connection).Error
}
func (q *GitHubConnectionRepository) EnsureGitHubConnectionUniqueness(ctx context.Context) error {
	database := q.db.WithContext(ctx)
	table := database.NamingStrategy.TableName("GitHubConnection")
	if err := database.Exec("DELETE FROM " + table + " WHERE id NOT IN (SELECT MIN(id) FROM " + table + " GROUP BY mcp_account_id, installation_id)").Error; err != nil {
		return fmt.Errorf("deduplicate GitHub connections: %w", err)
	}
	if err := database.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_github_connection_account_installation ON " + table + " (mcp_account_id, installation_id)").Error; err != nil {
		return fmt.Errorf("create GitHub connection uniqueness index: %w", err)
	}
	return nil
}
