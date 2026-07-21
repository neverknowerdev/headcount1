package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// PasswordResetTokenLifetime bounds how long an emailed reset link works.
const PasswordResetTokenLifetime = time.Hour

// CreatePasswordResetToken stores the hash of a fresh reset token and
// invalidates any prior unused tokens for the user (only the newest emailed
// link works).
func (q *Queries) CreatePasswordResetToken(ctx context.Context, userID int32, tokenHash string) (PasswordResetToken, error) {
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
func (q *Queries) ConsumePasswordResetToken(ctx context.Context, tokenHash string) (User, error) {
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
