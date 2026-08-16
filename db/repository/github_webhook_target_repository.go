package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
	"time"
)

type GitHubWebhookTargetRepository struct{ db *gorm.DB }

func NewGitHubWebhookTargetRepository(db *gorm.DB) *GitHubWebhookTargetRepository {
	return &GitHubWebhookTargetRepository{db: db}
}
func (q *GitHubWebhookTargetRepository) ListPendingGitHubWebhookTargets(ctx context.Context, deliveryID string) ([]GitHubWebhookTarget, error) {
	var targets []GitHubWebhookTarget
	err := q.db.WithContext(ctx).Where("delivery_id = ? AND wake_status <> ?", deliveryID, "completed").Find(&targets).Error
	return targets, err
}
func (q *GitHubWebhookTargetRepository) ClaimGitHubWebhookTarget(ctx context.Context, targetID int32, attemptToken string, now, leaseUntil time.Time) (bool, error) {
	claim := q.db.WithContext(ctx).Model(&GitHubWebhookTarget{}).Where("id = ? AND wake_status <> ? AND (wake_status <> ? OR wake_lease_expires_at IS NULL OR wake_lease_expires_at <= ?)", targetID, "completed", "processing", now).Updates(map[string]any{"wake_status": "processing", "wake_attempt_token": attemptToken, "wake_lease_expires_at": &leaseUntil, "wake_attempts": gorm.Expr("wake_attempts + 1"), "wake_last_error": ""})
	return claim.RowsAffected == 1, claim.Error
}
func (q *GitHubWebhookTargetRepository) UpdateGitHubWebhookTarget(ctx context.Context, targetID int32, attemptToken string, values map[string]any) error {
	result := q.db.WithContext(ctx).Model(&GitHubWebhookTarget{}).Where("id = ? AND wake_attempt_token = ?", targetID, attemptToken).Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrGitHubWebhookLeaseLost
	}
	return nil
}
func (q *GitHubWebhookTargetRepository) CountPendingGitHubWebhookTargets(ctx context.Context, deliveryID string) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).Model(&GitHubWebhookTarget{}).Where("delivery_id = ? AND wake_status <> ?", deliveryID, "completed").Count(&count).Error
	return count, err
}
