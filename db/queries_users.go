package db

import (
	"context"
	"strings"
	"time"
)

// NormalizeEmail canonicalizes an email for storage and lookup.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (q *Queries) CreateUser(ctx context.Context, email string) (User, error) {
	u := User{Email: NormalizeEmail(email)}
	err := q.db.WithContext(ctx).Create(&u).Error
	return u, err
}

func (q *Queries) GetUser(ctx context.Context, id int32) (User, error) {
	var u User
	err := q.db.WithContext(ctx).First(&u, id).Error
	return u, err
}

func (q *Queries) GetUserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := q.db.WithContext(ctx).Where("email = ?", NormalizeEmail(email)).First(&u).Error
	return u, err
}

func (q *Queries) ListUsers(ctx context.Context) ([]User, error) {
	var users []User
	err := q.db.WithContext(ctx).Order("id").Find(&users).Error
	return users, err
}

// SetUserReenrollTicket records the hashed re-enroll ticket and its expiry on a
// just-recovered account, gating the re-enrollment that follows recovery.
func (q *Queries) SetUserReenrollTicket(ctx context.Context, userID int32, tokenHash string, expiresAt time.Time) error {
	return q.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).
		Updates(map[string]any{"reenroll_token_hash": tokenHash, "reenroll_expires_at": expiresAt}).Error
}

// ClearUserReenrollTicket drops the re-enroll ticket once re-enrollment
// completes (or is no longer pending).
func (q *Queries) ClearUserReenrollTicket(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Model(&User{}).Where("id = ?", userID).
		Updates(map[string]any{"reenroll_token_hash": "", "reenroll_expires_at": nil}).Error
}
