package repository

import (
	"context"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type WebAuthnCredentialRepository struct{ db *gorm.DB }

func NewWebAuthnCredentialRepository(db *gorm.DB) *WebAuthnCredentialRepository {
	return &WebAuthnCredentialRepository{db: db}
}

// ── credentials ──────────────────────────────────────────────────────────────

func (q *WebAuthnCredentialRepository) CreateWebAuthnCredential(ctx context.Context, c WebAuthnCredential) (WebAuthnCredential, error) {
	err := q.db.WithContext(ctx).Create(&c).Error
	return c, err
}

func (q *WebAuthnCredentialRepository) ListCredentialsForUser(ctx context.Context, userID int32) ([]WebAuthnCredential, error) {
	var creds []WebAuthnCredential
	err := q.db.WithContext(ctx).Where("user_id = ?", userID).Order("id").Find(&creds).Error
	return creds, err
}

func (q *WebAuthnCredentialRepository) GetCredentialByCredentialID(ctx context.Context, credentialID []byte) (WebAuthnCredential, error) {
	var c WebAuthnCredential
	err := q.db.WithContext(ctx).Where("credential_id = ?", credentialID).First(&c).Error
	return c, err
}

// UpdateCredentialUsage records the authenticator sign counter and last-used
// time after a successful assertion (sign-count regression is the caller's to
// detect via the returned prior value).
func (q *WebAuthnCredentialRepository) UpdateCredentialUsage(ctx context.Context, id int32, signCount uint32, backupState bool) error {
	return q.db.WithContext(ctx).Model(&WebAuthnCredential{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{"sign_count": signCount, "backup_state": backupState, "last_used_at": time.Now()}).Error
}

func (q *WebAuthnCredentialRepository) RenameCredential(ctx context.Context, id, userID int32, nickname string) error {
	return q.db.WithContext(ctx).Model(&WebAuthnCredential{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("nickname", nickname).Error
}

func (q *WebAuthnCredentialRepository) DeleteCredential(ctx context.Context, id, userID int32) error {
	return q.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&WebAuthnCredential{}).Error
}

// DeleteCredentialsForUser removes every passkey of a user. Used by account
// recovery — with the last credential gone the DEK is unrecoverable, so the
// caller must also null the user's secret columns (crypto-shred).
func (q *WebAuthnCredentialRepository) DeleteCredentialsForUser(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&WebAuthnCredential{}).Error
}

// CryptoShredUser implements account recovery: it deletes the user's passkeys
// (making the DEK unrecoverable) and nulls every secret column they own, so
// the now-undecryptable "enc:u1:" ciphertext is cleared rather than left as
// dead weight. The user row, team memberships, companies, and tasks are all
// preserved — the account survives; only the stored secrets are wiped and must
// be re-entered after enrolling a fresh passkey.
func (q *WebAuthnCredentialRepository) CryptoShredUser(ctx context.Context, userID int32) error {
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
		return nil
	})
}
