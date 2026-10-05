package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
	"time"
)

type GitHubOAuthStateRepository struct{ db *gorm.DB }

func NewGitHubOAuthStateRepository(db *gorm.DB) *GitHubOAuthStateRepository {
	return &GitHubOAuthStateRepository{db: db}
}
func (q *GitHubOAuthStateRepository) CreateGitHubOAuthState(ctx context.Context, state GitHubOAuthState) error {
	return q.db.WithContext(ctx).Create(&state).Error
}
func (q *GitHubOAuthStateRepository) GetGitHubOAuthState(ctx context.Context, stateID string) (GitHubOAuthState, error) {
	var state GitHubOAuthState
	err := q.db.WithContext(ctx).First(&state, "id = ?", stateID).Error
	return state, err
}
func (q *GitHubOAuthStateRepository) ClaimGitHubOAuthState(ctx context.Context, stateID string, now time.Time) (bool, error) {
	claim := q.db.WithContext(ctx).Model(&GitHubOAuthState{}).Where("id = ? AND used_at IS NULL AND expires_at > ?", stateID, now).Update("used_at", now)
	return claim.RowsAffected == 1, claim.Error
}
