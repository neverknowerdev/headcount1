package db

import (
	"context"
	"time"
)

// WebAuthnChallengeLifetime bounds an in-flight ceremony (begin → finish).
const WebAuthnChallengeLifetime = 5 * time.Minute

// ── credentials ──────────────────────────────────────────────────────────────

func (q *Queries) CreateWebAuthnCredential(ctx context.Context, c WebAuthnCredential) (WebAuthnCredential, error) {
	err := q.db.WithContext(ctx).Create(&c).Error
	return c, err
}

func (q *Queries) ListCredentialsForUser(ctx context.Context, userID int32) ([]WebAuthnCredential, error) {
	var creds []WebAuthnCredential
	err := q.db.WithContext(ctx).Where("user_id = ?", userID).Order("id").Find(&creds).Error
	return creds, err
}

func (q *Queries) GetCredentialByCredentialID(ctx context.Context, credentialID []byte) (WebAuthnCredential, error) {
	var c WebAuthnCredential
	err := q.db.WithContext(ctx).Where("credential_id = ?", credentialID).First(&c).Error
	return c, err
}

// UpdateCredentialUsage records the authenticator sign counter and last-used
// time after a successful assertion (sign-count regression is the caller's to
// detect via the returned prior value).
func (q *Queries) UpdateCredentialUsage(ctx context.Context, id int32, signCount uint32) error {
	return q.db.WithContext(ctx).Model(&WebAuthnCredential{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{"sign_count": signCount, "last_used_at": time.Now()}).Error
}

func (q *Queries) RenameCredential(ctx context.Context, id, userID int32, nickname string) error {
	return q.db.WithContext(ctx).Model(&WebAuthnCredential{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("nickname", nickname).Error
}

func (q *Queries) DeleteCredential(ctx context.Context, id, userID int32) error {
	return q.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&WebAuthnCredential{}).Error
}

// DeleteCredentialsForUser removes every passkey of a user. Used by account
// recovery — with the last credential gone the DEK is unrecoverable, so the
// caller must also null the user's secret columns (crypto-shred).
func (q *Queries) DeleteCredentialsForUser(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&WebAuthnCredential{}).Error
}

// ── ceremony challenges ──────────────────────────────────────────────────────

func (q *Queries) CreateWebAuthnSession(ctx context.Context, userID *int32, purpose, data string) (WebAuthnSession, error) {
	s := WebAuthnSession{
		UserID:    userID,
		Purpose:   purpose,
		Data:      data,
		ExpiresAt: time.Now().Add(WebAuthnChallengeLifetime),
	}
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

// ConsumeWebAuthnSession fetches an unexpired ceremony challenge and deletes it
// (single-use), returning its stored session data.
func (q *Queries) ConsumeWebAuthnSession(ctx context.Context, id int32, purpose string) (WebAuthnSession, error) {
	var s WebAuthnSession
	if err := q.db.WithContext(ctx).
		Where("id = ? AND purpose = ? AND expires_at > ?", id, purpose, time.Now()).
		First(&s).Error; err != nil {
		return WebAuthnSession{}, err
	}
	q.db.WithContext(ctx).Delete(&WebAuthnSession{}, s.ID)
	return s, nil
}

func (q *Queries) DeleteExpiredWebAuthnSessions(ctx context.Context) error {
	return q.db.WithContext(ctx).Where("expires_at <= ?", time.Now()).Delete(&WebAuthnSession{}).Error
}
