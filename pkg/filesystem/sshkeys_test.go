package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResolveSSHKeyPath(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.WebAuthnCredential{}, &db.UserGitCredential{}))
	q := db.New(database)
	ctx := context.Background()
	base := t.TempDir()

	user, err := q.CreateUser(ctx, "keys@test.local")
	require.NoError(t, err)

	// No personal key → shared/global key path.
	require.Equal(t, NewPaths(base).SSHKeyFile(), ResolveSSHKeyPath(ctx, q, base, user.ID))

	// Store a personal key while unlocked.
	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(user.ID, dek, time.Minute)
	require.NoError(t, q.UpsertUserSSHKey(ctx, user.ID, "PRIVATEKEY"))

	// Unlocked + present → the per-user materialized key (0600), containing the
	// decrypted bytes.
	path := ResolveSSHKeyPath(ctx, q, base, user.ID)
	require.Equal(t, NewPaths(base).UserSSHKeyFile(user.ID), path)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "PRIVATEKEY", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Locked → falls back to the shared key (the personal key is undecryptable).
	secrets.Default().LockUser(user.ID)
	require.Equal(t, NewPaths(base).SSHKeyFile(), ResolveSSHKeyPath(ctx, q, base, user.ID))

	// userID 0 (no owner) → shared key.
	require.Equal(t, filepath.Join(base, "ssh", "id_rsa"), ResolveSSHKeyPath(ctx, q, base, 0))
}
