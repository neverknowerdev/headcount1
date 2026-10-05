package repository

import (
	"context"
	"strings"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type UserRepository struct{ db *gorm.DB }

func NewUserRepository(db *gorm.DB) *UserRepository { return &UserRepository{db: db} }

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (q *UserRepository) CreateUser(ctx context.Context, email string) (User, error) {
	var u User
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&User{}).Count(&count).Error; err != nil {
			return err
		}
		u = User{Email: NormalizeEmail(email), IsAdmin: count == 0}
		return tx.Create(&u).Error
	})
	return u, err
}

func (q *UserRepository) GetUser(ctx context.Context, id int32) (User, error) {
	var u User
	err := q.db.WithContext(ctx).First(&u, id).Error
	return u, err
}

func (q *UserRepository) GetUserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := q.db.WithContext(ctx).Where("email = ?", NormalizeEmail(email)).First(&u).Error
	return u, err
}

func (q *UserRepository) ListUsers(ctx context.Context) ([]User, error) {
	var users []User
	err := q.db.WithContext(ctx).Order("id").Find(&users).Error
	return users, err
}

// SetUserReenrollTicket records the hashed re-enroll ticket and its expiry on a
// just-recovered account, gating the re-enrollment that follows recovery.
func (q *UserRepository) SetUserReenrollTicket(ctx context.Context, userID int32, tokenHash string, expiresAt time.Time) error {
	return q.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).
		Updates(map[string]any{"reenroll_token_hash": tokenHash, "reenroll_expires_at": expiresAt}).Error
}

// ClearUserReenrollTicket drops the re-enroll ticket once re-enrollment
// completes (or is no longer pending).
func (q *UserRepository) ClearUserReenrollTicket(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).
		Updates(map[string]any{"reenroll_token_hash": "", "reenroll_expires_at": nil}).Error
}
