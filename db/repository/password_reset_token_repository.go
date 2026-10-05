package repository

import (
	"context"
	"fmt"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type PasswordResetTokenRepository struct{ db *gorm.DB }

func NewPasswordResetTokenRepository(db *gorm.DB) *PasswordResetTokenRepository {
	return &PasswordResetTokenRepository{db: db}
}

const PasswordResetTokenLifetime = time.Hour

func (q *PasswordResetTokenRepository) CreatePasswordResetToken(ctx context.Context, userID int32, tokenHash string) (PasswordResetToken, error) {
	t := PasswordResetToken{
		TokenHash: tokenHash,
		UserID:    userID,
		ExpiresAt: time.Now().Add(PasswordResetTokenLifetime),
	}
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&PasswordResetToken{}).
			Where("user_id = ? AND used_at IS NULL", userID).
			Update("used_at", &now).Error; err != nil {
			return err
		}
		return tx.Create(&t).Error
	})
	return t, err
}

// ConsumePasswordResetToken atomically validates and burns a reset token,
// returning the user it belongs to. A token works exactly once and only
// before its expiry.
func (q *PasswordResetTokenRepository) ConsumePasswordResetToken(ctx context.Context, tokenHash string) (User, error) {
	var user User
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t PasswordResetToken
		if err := tx.
			Where("token_hash = ? AND used_at IS NULL AND expires_at > ?", tokenHash, time.Now()).
			First(&t).Error; err != nil {
			return fmt.Errorf("invalid or expired reset token")
		}
		now := time.Now()
		if err := tx.Model(&PasswordResetToken{}).Where("id = ?", t.ID).
			Update("used_at", &now).Error; err != nil {
			return err
		}
		return tx.First(&user, t.UserID).Error
	})
	return user, err
}
