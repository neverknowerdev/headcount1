package db_test

import (
	"context"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUserGitCredentialEncryptedPerUser(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.WebAuthnCredential{}, &db.UserGitCredential{}))
	q := db.New(database)
	ctx := context.Background()

	user, err := q.CreateUser(ctx, "git@test.local")
	require.NoError(t, err)
	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(user.ID, dek, time.Minute)

	const key = "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----"
	require.NoError(t, q.UpsertUserSSHKey(ctx, user.ID, key))

	// Stored as ciphertext (enc:u1), never raw.
	var raw string
	require.NoError(t, database.Raw("SELECT ssh_private_key FROM user_git_credentials WHERE user_id = ?", user.ID).Scan(&raw).Error)
	require.True(t, secrets.IsSealed(raw), "ssh key must be sealed: %q", raw)
	require.NotContains(t, raw, "secret")

	// Decrypts while unlocked.
	got, err := q.GetUserGitCredential(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, key, got.SSHPrivateKey)

	// Upsert replaces the key (and never blanks it).
	require.NoError(t, q.UpsertUserSSHKey(ctx, user.ID, "new-key"))
	got, _ = q.GetUserGitCredential(ctx, user.ID)
	require.Equal(t, "new-key", got.SSHPrivateKey)

	// Locked → the key reads back empty (undecryptable), not plaintext.
	secrets.Default().LockUser(user.ID)
	locked, err := q.GetUserGitCredential(ctx, user.ID)
	require.NoError(t, err)
	require.Empty(t, locked.SSHPrivateKey)
}
