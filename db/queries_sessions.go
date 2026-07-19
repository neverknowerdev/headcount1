package db

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// SessionLifetime is the long-lived ceiling used for the in-memory keyring TTL
// and the graceful-exit keyring snapshot — NOT for access sessions, which are
// short (AccessTokenLifetime) and refreshed via a rotating refresh token.
const SessionLifetime = 30 * 24 * time.Hour

// sessionRenewAfter bounds how often an access session's expiry is bumped, so
// an active session stays alive without rewriting the row on every request.
const sessionRenewAfter = 15 * time.Minute

// CreateSession mints a short-lived access session (AccessTokenLifetime) under
// an absolute ceiling inherited from the refresh-token family. Once the sliding
// window lapses the browser exchanges its refresh token at /auth/refresh for a
// new pair; family reuse-detection there is what makes a stolen token
// containable. The absolute ceiling bounds a directly-used (never-refreshed)
// access cookie so it cannot renew forever.
func (q *Queries) CreateSession(ctx context.Context, userID int32, tokenHash string, absoluteExpiry time.Time) (Session, error) {
	s := Session{
		TokenHash:         tokenHash,
		UserID:            userID,
		ExpiresAt:         time.Now().Add(AccessTokenLifetime),
		AbsoluteExpiresAt: absoluteExpiry,
	}
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

// GetSessionUser resolves an unexpired access session (by token hash) to its
// user, sliding the expiry forward at most once per sessionRenewAfter so an
// active session survives without a refresh round-trip. The slide never crosses
// the session's absolute ceiling, and an already-past ceiling rejects the
// session outright (and deletes it) even if the sliding window is still open.
func (q *Queries) GetSessionUser(ctx context.Context, tokenHash string) (User, error) {
	var s Session
	if err := q.db.WithContext(ctx).
		Where("token_hash = ? AND expires_at > ?", tokenHash, time.Now()).
		First(&s).Error; err != nil {
		return User{}, err
	}
	now := time.Now()
	// Hard ceiling: an access cookie can slide only within the family's
	// absolute cap. Past it, the session is dead regardless of the 1h window.
	if !s.AbsoluteExpiresAt.IsZero() && now.After(s.AbsoluteExpiresAt) {
		q.db.WithContext(ctx).Where("id = ?", s.ID).Delete(&Session{})
		return User{}, gorm.ErrRecordNotFound
	}
	if newExpiry := now.Add(AccessTokenLifetime); newExpiry.Sub(s.ExpiresAt) > sessionRenewAfter {
		// Never slide past the absolute ceiling.
		if !s.AbsoluteExpiresAt.IsZero() && newExpiry.After(s.AbsoluteExpiresAt) {
			newExpiry = s.AbsoluteExpiresAt
		}
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
