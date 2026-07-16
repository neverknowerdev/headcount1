package db

import (
	"context"
	"strings"
)

// NormalizeEmail canonicalizes an email for storage and lookup.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (q *Queries) CreateUser(ctx context.Context, email, passwordHash string) (User, error) {
	u := User{Email: NormalizeEmail(email), PasswordHash: passwordHash}
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

func (q *Queries) UpdateUserPassword(ctx context.Context, id int32, passwordHash string) error {
	return q.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).
		Update("password_hash", passwordHash).Error
}
