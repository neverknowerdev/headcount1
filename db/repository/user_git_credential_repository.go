package repository

import (
	"context"

	"agent-orchestrator/pkg/secrets"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type UserGitCredentialRepository struct{ db *gorm.DB }

func NewUserGitCredentialRepository(db *gorm.DB) *UserGitCredentialRepository {
	return &UserGitCredentialRepository{db: db}
}

func (q *UserGitCredentialRepository) GetUserGitCredential(ctx context.Context, userID int32) (UserGitCredential, error) {
	var c UserGitCredential
	err := q.db.WithContext(ctx).Where("user_id = ?", userID).First(&c).Error
	return c, err
}

// UpsertUserSSHKey stores (or replaces) a user's SSH private key. The plaintext
// key is sealed here — at the point of write, close to the caller — so nothing
// downstream ever holds it. Returns secrets.ErrLocked if the user's vault is
// locked (nothing to seal against). Empty keys are rejected by the caller.
func (q *UserGitCredentialRepository) UpsertUserSSHKey(ctx context.Context, userID int32, sshKey string) error {
	sealed, err := secrets.Default().EncryptForUser(userID, sshKey)
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
func (q *UserGitCredentialRepository) DeleteUserGitCredential(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&UserGitCredential{}).Error
}
