package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"errors"
	"gorm.io/gorm"
	"time"
)

type GitHubWebhookDeliveryRepository struct{ db *gorm.DB }

func NewGitHubWebhookDeliveryRepository(db *gorm.DB) *GitHubWebhookDeliveryRepository {
	return &GitHubWebhookDeliveryRepository{db: db}
}
func (q *GitHubWebhookDeliveryRepository) ClaimGitHubWebhookDelivery(ctx context.Context, deliveryID, event, attemptToken string, now, leaseUntil time.Time) (GitHubWebhookDelivery, bool, error) {
	var delivery GitHubWebhookDelivery
	err := q.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		delivery = GitHubWebhookDelivery{DeliveryID: deliveryID, Event: event, Status: "pending"}
		if createErr := q.db.WithContext(ctx).Create(&delivery).Error; createErr != nil {
			if loadErr := q.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error; loadErr != nil {
				return GitHubWebhookDelivery{}, false, createErr
			}
		}
	} else if err != nil {
		return GitHubWebhookDelivery{}, false, err
	}
	if delivery.Status == "completed" {
		return delivery, true, nil
	}
	claim := q.db.WithContext(ctx).Model(&GitHubWebhookDelivery{}).Where("delivery_id = ? AND status <> ? AND (status <> ? OR lease_expires_at IS NULL OR lease_expires_at <= ?)", deliveryID, "completed", "processing", now).Updates(map[string]any{"status": "processing", "attempt_token": attemptToken, "lease_expires_at": &leaseUntil, "attempts": gorm.Expr("attempts + 1"), "last_error": ""})
	if claim.Error != nil {
		return GitHubWebhookDelivery{}, false, claim.Error
	}
	if claim.RowsAffected == 0 {
		if err := q.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error; err != nil {
			return GitHubWebhookDelivery{}, false, err
		}
		if delivery.Status == "completed" {
			return delivery, true, nil
		}
		return GitHubWebhookDelivery{}, false, ErrGitHubWebhookAlreadyProcessing
	}
	if err := q.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error; err != nil {
		return GitHubWebhookDelivery{}, false, err
	}
	return delivery, false, nil
}
func (q *GitHubWebhookDeliveryRepository) UpdateGitHubWebhookDelivery(ctx context.Context, deliveryID, attemptToken string, values map[string]any) error {
	result := q.db.WithContext(ctx).Model(&GitHubWebhookDelivery{}).Where("delivery_id = ? AND attempt_token = ?", deliveryID, attemptToken).Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrGitHubWebhookLeaseLost
	}
	return nil
}
