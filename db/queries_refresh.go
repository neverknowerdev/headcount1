package db

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// AccessTokenLifetime is the short sliding window of an access session.
const AccessTokenLifetime = time.Hour

// refreshTokenLifetime is the rotating refresh token's own expiry; the family's
// AbsoluteExpiresAt caps how long rotation can continue.
const refreshTokenLifetime = 24 * time.Hour

// ErrRefreshReuse is returned when an already-used (or revoked) refresh token
// is presented — a theft signal. The caller revokes the family and rejects.
var ErrRefreshReuse = errors.New("refresh token reuse detected")

// refreshReuseGracePeriod tolerates concurrent legitimate refreshes: two tabs
// or two devices sharing a refresh cookie can each fire /auth/refresh with the
// same old token at nearly the same instant. Within this window, replaying a
// just-used (not revoked) token mints a fresh successor instead of tripping
// theft detection — so a routine race doesn't force-log-out the user. A genuine
// stolen-token replay lands far outside this window and still burns the family.
const refreshReuseGracePeriod = 30 * time.Second

// SessionAbsoluteCap is the hard ceiling on a refresh-token family, after which
// the user must re-authenticate. Configurable via SESSION_ABSOLUTE_CAP (days).
func SessionAbsoluteCap() time.Duration {
	days := 14
	if v := os.Getenv("SESSION_ABSOLUTE_CAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 60 {
			days = n
		}
	}
	return time.Duration(days) * 24 * time.Hour
}

// SessionReauthGap is the safety buffer before the absolute session cap during
// which the UI proactively asks the user to re-authenticate. Re-auth resets the
// cap (a fresh token family), so a long-lived login never lapses mid-run
// without warning — the whole point is that background agents keep decrypting
// secrets and never hit an abrupt, silent forced-logout wall.
//
// Configurable via SESSION_REAUTH_GAP (days); clamped to [1, cap-1] so the soft
// re-auth deadline (cap − gap) always falls strictly inside the session, before
// the hard ceiling.
func SessionReauthGap() time.Duration {
	capDays := int(SessionAbsoluteCap() / (24 * time.Hour))
	gap := 4
	if v := os.Getenv("SESSION_REAUTH_GAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			gap = n
		}
	}
	if gap > capDays-1 {
		gap = capDays - 1
	}
	if gap < 1 {
		gap = 1
	}
	return time.Duration(gap) * 24 * time.Hour
}

// GetSessionAbsoluteExpiry returns the latest absolute expiry among the user's
// live (non-revoked, unexpired) refresh tokens — i.e. when the current login's
// hard ceiling falls. Returns the zero time when the user has no active refresh
// session (e.g. the E2E fixture or a cookieless client), which callers treat as
// "nothing to report".
func (q *Queries) GetSessionAbsoluteExpiry(ctx context.Context, userID int32) (time.Time, error) {
	var rt RefreshToken
	err := q.db.WithContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL AND absolute_expires_at > ?", userID, time.Now()).
		Order("absolute_expires_at DESC").
		First(&rt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return rt.AbsoluteExpiresAt, nil
}

// CreateRefreshToken mints the first token of a new family (fresh login).
func (q *Queries) CreateRefreshToken(ctx context.Context, userID int32, familyID, tokenHash string, absoluteExpiry time.Time) (RefreshToken, error) {
	rt := RefreshToken{
		FamilyID:          familyID,
		TokenHash:         tokenHash,
		UserID:            userID,
		ExpiresAt:         time.Now().Add(refreshTokenLifetime),
		AbsoluteExpiresAt: absoluteExpiry,
	}
	err := q.db.WithContext(ctx).Create(&rt).Error
	return rt, err
}

// RotateRefreshToken validates a presented refresh token and, on success, marks
// it used and mints its successor in the same family. Reuse of an already-used
// or revoked token revokes the whole family and returns ErrRefreshReuse.
func (q *Queries) RotateRefreshToken(ctx context.Context, oldHash, newHash string) (RefreshToken, error) {
	var next RefreshToken
	var reuse bool
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cur RefreshToken
		if err := tx.Where("token_hash = ?", oldHash).First(&cur).Error; err != nil {
			return err
		}
		now := time.Now()
		if cur.RevokedAt != nil {
			// A revoked token means the family was already burned (theft, logout,
			// or a prior reuse). Reject; do not resurrect it.
			reuse = true
			if err := tx.Where("user_id = ?", cur.UserID).Delete(&Session{}).Error; err != nil {
				return err
			}
			return tx.Model(&RefreshToken{}).Where("family_id = ? AND revoked_at IS NULL", cur.FamilyID).
				Update("revoked_at", now).Error
		}
		if cur.UsedAt != nil {
			if now.Sub(*cur.UsedAt) > refreshReuseGracePeriod {
				// Replay of a long-spent token → theft. Burn the whole family AND
				// every live access session for the user, so a thief who grabbed
				// only the access cookie loses access the moment the theft is
				// detected (instead of keeping it until the sliding window lapses).
				// Commit the revocation (return nil, not the sentinel, so the
				// transaction is NOT rolled back) and signal reuse afterwards.
				reuse = true
				if err := tx.Where("user_id = ?", cur.UserID).Delete(&Session{}).Error; err != nil {
					return err
				}
				return tx.Model(&RefreshToken{}).Where("family_id = ? AND revoked_at IS NULL", cur.FamilyID).
					Update("revoked_at", now).Error
			}
			// Within the grace window: a concurrent legitimate refresh. Mint a
			// fresh successor in the same family without burning it, still
			// respecting the absolute cap.
			if now.After(cur.AbsoluteExpiresAt) {
				return gorm.ErrRecordNotFound
			}
			next = RefreshToken{
				FamilyID:          cur.FamilyID,
				TokenHash:         newHash,
				UserID:            cur.UserID,
				ExpiresAt:         now.Add(refreshTokenLifetime),
				AbsoluteExpiresAt: cur.AbsoluteExpiresAt,
			}
			return tx.Create(&next).Error
		}
		if now.After(cur.ExpiresAt) || now.After(cur.AbsoluteExpiresAt) {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Model(&cur).Update("used_at", now).Error; err != nil {
			return err
		}
		next = RefreshToken{
			FamilyID:          cur.FamilyID,
			TokenHash:         newHash,
			UserID:            cur.UserID,
			ExpiresAt:         now.Add(refreshTokenLifetime),
			AbsoluteExpiresAt: cur.AbsoluteExpiresAt,
		}
		return tx.Create(&next).Error
	})
	if err != nil {
		return RefreshToken{}, err
	}
	if reuse {
		return RefreshToken{}, ErrRefreshReuse
	}
	return next, err
}

// RevokeRefreshFamily kills every token in a family (logout of one lineage).
func (q *Queries) RevokeRefreshFamily(ctx context.Context, familyID string) error {
	return q.db.WithContext(ctx).Model(&RefreshToken{}).
		Where("family_id = ? AND revoked_at IS NULL", familyID).
		Update("revoked_at", time.Now()).Error
}

// RevokeRefreshTokensForUser revokes all of a user's refresh tokens.
func (q *Queries) RevokeRefreshTokensForUser(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&RefreshToken{}).Error
}

// DeleteExpiredRefreshTokens GCs spent/expired rows.
func (q *Queries) DeleteExpiredRefreshTokens(ctx context.Context) error {
	return q.db.WithContext(ctx).
		Where("absolute_expires_at <= ? OR revoked_at IS NOT NULL", time.Now()).
		Delete(&RefreshToken{}).Error
}
