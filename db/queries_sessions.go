package db

import (
	"context"
	"time"
)

// SessionLifetime is the long-lived ceiling used for the in-memory keyring TTL
// and the graceful-exit keyring snapshot — NOT for access sessions, which are
// short (AccessTokenLifetime) and refreshed via a rotating refresh token.
const SessionLifetime = 30 * 24 * time.Hour

// sessionRenewAfter bounds how often an access session's expiry is bumped, so
// an active session stays alive without rewriting the row on every request.
const sessionRenewAfter = 15 * time.Minute

// CreateSession mints a short-lived access session (AccessTokenLifetime). Once
// it lapses the browser exchanges its refresh token at /auth/refresh for a new
// pair; family reuse-detection there is what makes a stolen token containable.
func (q *Queries) CreateSession(ctx context.Context, userID int32, tokenHash string) (Session, error) {
	s := Session{
		TokenHash: tokenHash,
		UserID:    userID,
		ExpiresAt: time.Now().Add(AccessTokenLifetime),
	}
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

// GetSessionUser resolves an unexpired access session (by token hash) to its
// user, sliding the expiry forward at most once per sessionRenewAfter so an
// active session survives without a refresh round-trip.
func (q *Queries) GetSessionUser(ctx context.Context, tokenHash string) (User, error) {
	var s Session
	if err := q.db.WithContext(ctx).
		Where("token_hash = ? AND expires_at > ?", tokenHash, time.Now()).
		First(&s).Error; err != nil {
		return User{}, err
	}
	if newExpiry := time.Now().Add(AccessTokenLifetime); newExpiry.Sub(s.ExpiresAt) > sessionRenewAfter {
		q.db.WithContext(ctx).Model(&Session{}).Where("id = ?", s.ID).Update("expires_at", newExpiry)
	}
	var u User
	if err := q.db.WithContext(ctx).First(&u, s.UserID).Error; err != nil {
		return User{}, err
	}
	return u, nil
}

func (q *Queries) DeleteSessionByTokenHash(ctx context.Context, tokenHash string) error {
	return q.db.WithContext(ctx).Where("token_hash = ?", tokenHash).Delete(&Session{}).Error
}

// DeleteSessionsForUser revokes every session of a user — called on password
// change/reset so stolen cookies die with the old password.
func (q *Queries) DeleteSessionsForUser(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&Session{}).Error
}

func (q *Queries) DeleteExpiredSessions(ctx context.Context) error {
	return q.db.WithContext(ctx).Where("expires_at <= ?", time.Now()).Delete(&Session{}).Error
}
