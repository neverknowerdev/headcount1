package repository

import (
	"context"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type WebAuthnSessionRepository struct{ db *gorm.DB }

func NewWebAuthnSessionRepository(db *gorm.DB) *WebAuthnSessionRepository {
	return &WebAuthnSessionRepository{db: db}
}

const WebAuthnChallengeLifetime = 5 * time.Minute

func (r *WebAuthnSessionRepository) CreateWebAuthnSession(ctx context.Context, userID *int32, purpose, data string) (WebAuthnSession, error) {
	session := WebAuthnSession{UserID: userID, Purpose: purpose, Data: data, ExpiresAt: time.Now().Add(WebAuthnChallengeLifetime)}
	err := r.db.WithContext(ctx).Create(&session).Error
	return session, err
}

func (r *WebAuthnSessionRepository) ConsumeWebAuthnSession(ctx context.Context, id int32, purpose string, expectUserID *int32) (WebAuthnSession, error) {
	var session WebAuthnSession
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where("id = ? AND purpose = ? AND expires_at > ?", id, purpose, time.Now())
		if expectUserID != nil {
			query = query.Where("user_id = ?", *expectUserID)
		}
		if err := query.First(&session).Error; err != nil {
			return err
		}
		return tx.Delete(&WebAuthnSession{}, session.ID).Error
	})
	if err != nil {
		return WebAuthnSession{}, err
	}
	return session, nil
}

func (r *WebAuthnSessionRepository) DeleteExpiredWebAuthnSessions(ctx context.Context) error {
	return r.db.WithContext(ctx).Where("expires_at <= ?", time.Now()).Delete(&WebAuthnSession{}).Error
}
