package db

import (
	"context"

	"gorm.io/gorm/clause"
)

// GetUserGitCredential returns a user's git credentials. SSHPrivateKey/GitHubPAT
// are decrypted transparently by the serializer when the user's vault is
// unlocked; when locked they read back empty (the serializer degrades locked
// reads), so callers should gate on secrets.IsUnlocked before relying on them.
func (q *Queries) GetUserGitCredential(ctx context.Context, userID int32) (UserGitCredential, error) {
	var c UserGitCredential
	err := q.db.WithContext(ctx).Where("user_id = ?", userID).First(&c).Error
	return c, err
}

// UpsertUserSSHKey stores (or replaces) a user's SSH private key. Empty keys are
// rejected by the caller; here we only ever write a non-empty key, so the secret
// column is always set to a real value (never blanked — see the A1 fix).
func (q *Queries) UpsertUserSSHKey(ctx context.Context, userID int32, sshKey string) error {
	row := UserGitCredential{UserID: &userID, SSHPrivateKey: sshKey}
	return q.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"ssh_private_key", "updated_at"}),
	}).Create(&row).Error
}

// DeleteUserGitCredential removes a user's git credentials (e.g. crypto-shred).
func (q *Queries) DeleteUserGitCredential(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&UserGitCredential{}).Error
}
