package db

import (
	"context"

	"agent-orchestrator/pkg/secrets"

	"gorm.io/gorm/clause"
)

// GetUserGitCredential returns a user's git credentials. SSHPrivateKeyEncrypted
// holds the sealed ciphertext verbatim (the serializer no longer decrypts on
// read); call UserGitCredential.DecryptSSHKey at the point of use to get the
// plaintext, which returns secrets.ErrLocked when the user's vault is locked.
func (q *Queries) GetUserGitCredential(ctx context.Context, userID int32) (UserGitCredential, error) {
	var c UserGitCredential
	err := q.db.WithContext(ctx).Where("user_id = ?", userID).First(&c).Error
	return c, err
}

// UpsertUserSSHKey stores (or replaces) a user's SSH private key. The plaintext
// key is sealed here — at the point of write, close to the caller — so nothing
// downstream ever holds it. Returns secrets.ErrLocked if the user's vault is
// locked (nothing to seal against). Empty keys are rejected by the caller.
func (q *Queries) UpsertUserSSHKey(ctx context.Context, userID int32, sshKey string) error {
	sealed, err := secrets.Default().SealForUser(userID, sshKey)
	if err != nil {
		return err
	}
	row := UserGitCredential{UserID: &userID, SSHPrivateKeyEncrypted: sealed}
	return q.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"ssh_private_key", "updated_at"}),
	}).Create(&row).Error
}

// DeleteUserGitCredential removes a user's git credentials (e.g. crypto-shred).
func (q *Queries) DeleteUserGitCredential(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&UserGitCredential{}).Error
}
