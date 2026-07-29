package db

import (
	"context"
	"time"

	"gorm.io/gorm"
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
func (q *Queries) UpdateCredentialUsage(ctx context.Context, id int32, signCount uint32, backupState bool) error {
	return q.db.WithContext(ctx).Model(&WebAuthnCredential{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{"sign_count": signCount, "backup_state": backupState, "last_used_at": time.Now()}).Error
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
// ConsumeWebAuthnSession atomically fetches and deletes a ceremony session. It
// runs the select+delete in one transaction so a session can be consumed only
// once even under concurrent finishes. When expectUserID is non-nil the lookup
// is additionally scoped to that user — used by the authenticated unlock/reauth
// ceremonies so a logged-in user can neither consume nor (by guessing the
// sequential id) delete another user's in-flight ceremony.
func (q *Queries) ConsumeWebAuthnSession(ctx context.Context, id int32, purpose string, expectUserID *int32) (WebAuthnSession, error) {
	var s WebAuthnSession
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where("id = ? AND purpose = ? AND expires_at > ?", id, purpose, time.Now())
		if expectUserID != nil {
			query = query.Where("user_id = ?", *expectUserID)
		}
		if err := query.First(&s).Error; err != nil {
			return err
		}
		return tx.Delete(&WebAuthnSession{}, s.ID).Error
	})
	if err != nil {
		return WebAuthnSession{}, err
	}
	return s, nil
}

func (q *Queries) DeleteExpiredWebAuthnSessions(ctx context.Context) error {
	return q.db.WithContext(ctx).Where("expires_at <= ?", time.Now()).Delete(&WebAuthnSession{}).Error
}

// CryptoShredUser implements account recovery: it deletes the user's passkeys
// (making the DEK unrecoverable) and nulls every secret column they own, so
// the now-undecryptable "enc:u1:" ciphertext is cleared rather than left as
// dead weight. The user row, team memberships, companies, and tasks are all
// preserved — the account survives; only the stored secrets are wiped and must
// be re-entered after enrolling a fresh passkey.
func (q *Queries) CryptoShredUser(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&WebAuthnCredential{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&LLMProvider{}).Where("user_id = ?", userID).Update("api_key", "").Error; err != nil {
			return err
		}
		if err := tx.Model(&MCPAccount{}).Where("user_id = ?", userID).Update("auth_token", "").Error; err != nil {
			return err
		}
		// The user's git credentials are sealed under the now-shredded DEK; drop
		// the row so no dead ciphertext lingers.
		if err := tx.Where("user_id = ?", userID).Delete(&UserGitCredential{}).Error; err != nil {
			return err
		}
		// Environment secrets this user sealed are equally unrecoverable —
		// drop them so environments show "no value" instead of dead ciphertext.
		if err := tx.Where("user_id = ?", userID).Delete(&EnvironmentSecret{}).Error; err != nil {
			return err
		}
		return nil
	})
}
