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

	// No personal key → shared/global key path, no-op cleanup.
	p, cleanup := ResolveSSHKeyPath(ctx, q, base, user.ID)
	require.Equal(t, NewPaths(base).SSHKeyFile(), p)
	cleanup()

	// Store a personal key while unlocked.
	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(user.ID, dek, time.Minute)
	require.NoError(t, q.UpsertUserSSHKey(ctx, user.ID, "PRIVATEKEY"))

	// Unlocked + present → an ephemeral materialized key (0600), containing the
	// decrypted bytes, under a per-op temp dir that the cleanup removes.
	path, cleanup := ResolveSSHKeyPath(ctx, q, base, user.ID)
	require.NotEqual(t, NewPaths(base).SSHKeyFile(), path)
	require.Equal(t, NewPaths(base).SSHDir(), filepath.Dir(filepath.Dir(path)))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "PRIVATEKEY", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Cleanup deletes the materialized key — the plaintext must not linger.
	cleanup()
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "ephemeral key must be removed on cleanup")

	// Locked → falls back to the shared key (the personal key is undecryptable).
	secrets.Default().LockUser(user.ID)
	p, cleanup = ResolveSSHKeyPath(ctx, q, base, user.ID)
	require.Equal(t, NewPaths(base).SSHKeyFile(), p)
	cleanup()

	// userID 0 (no owner) → shared key.
	p, cleanup = ResolveSSHKeyPath(ctx, q, base, 0)
	require.Equal(t, filepath.Join(base, "ssh", "id_rsa"), p)
	cleanup()
}
