package db_test

import (
	"context"
	"testing"
	"time"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupWebAuthnTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.WebAuthnCredential{}, &db.WebAuthnSession{}))
	return database
}

func TestWebAuthnCredentialCRUD(t *testing.T) {
	database := setupWebAuthnTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	user, err := q.CreateUser(ctx, "cred@test.local")
	require.NoError(t, err)

	c, err := q.CreateWebAuthnCredential(ctx, db.WebAuthnCredential{
		UserID: user.ID, CredentialID: []byte("cred-1"), PublicKey: []byte("pub"),
		WrappedDEK: "wrapped", PRFSalt: []byte("salt"), Nickname: "Laptop",
	})
	require.NoError(t, err)
	require.NotZero(t, c.ID)

	// Lookup by raw credential id (login path).
	got, err := q.GetCredentialByCredentialID(ctx, []byte("cred-1"))
	require.NoError(t, err)
	require.Equal(t, c.ID, got.ID)
	require.Equal(t, "wrapped", got.WrappedDEK)

	// List for user.
	list, err := q.ListCredentialsForUser(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// Sign-count + last-used update.
	require.NoError(t, q.UpdateCredentialUsage(ctx, c.ID, 42))
	got, _ = q.GetCredentialByCredentialID(ctx, []byte("cred-1"))
	require.Equal(t, uint32(42), got.SignCount)
	require.False(t, got.LastUsedAt.IsZero())

	// Rename.
	require.NoError(t, q.RenameCredential(ctx, c.ID, user.ID, "Phone"))
	list, _ = q.ListCredentialsForUser(ctx, user.ID)
	require.Equal(t, "Phone", list[0].Nickname)

	// Unique credential id enforced.
	_, err = q.CreateWebAuthnCredential(ctx, db.WebAuthnCredential{
		UserID: user.ID, CredentialID: []byte("cred-1"), PublicKey: []byte("p"), WrappedDEK: "w", PRFSalt: []byte("s"),
	})
	require.Error(t, err, "duplicate credential id must be rejected")
}

func TestDeleteCredentialsForUserCryptoShred(t *testing.T) {
	database := setupWebAuthnTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	user, _ := q.CreateUser(ctx, "shred@test.local")
	for i := 0; i < 3; i++ {
		_, err := q.CreateWebAuthnCredential(ctx, db.WebAuthnCredential{
			UserID: user.ID, CredentialID: []byte{byte(i)}, PublicKey: []byte("p"), WrappedDEK: "w", PRFSalt: []byte("s"),
		})
		require.NoError(t, err)
	}
	require.NoError(t, q.DeleteCredentialsForUser(ctx, user.ID))
	list, _ := q.ListCredentialsForUser(ctx, user.ID)
	require.Empty(t, list, "recovery must remove all passkeys")
}

func TestWebAuthnSessionSingleUseAndExpiry(t *testing.T) {
	database := setupWebAuthnTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	s, err := q.CreateWebAuthnSession(ctx, nil, "register", "session-data")
	require.NoError(t, err)

	// Consume once.
	got, err := q.ConsumeWebAuthnSession(ctx, s.ID, "register")
	require.NoError(t, err)
	require.Equal(t, "session-data", got.Data)

	// Second consume fails (single-use).
	_, err = q.ConsumeWebAuthnSession(ctx, s.ID, "register")
	require.Error(t, err)

	// Wrong purpose is rejected.
	s2, _ := q.CreateWebAuthnSession(ctx, nil, "login", "d")
	_, err = q.ConsumeWebAuthnSession(ctx, s2.ID, "register")
	require.Error(t, err, "purpose mismatch must not consume")

	// Expired challenge is rejected + GC'd.
	expired := db.WebAuthnSession{Purpose: "login", Data: "d", ExpiresAt: time.Now().Add(-time.Minute)}
	require.NoError(t, database.Create(&expired).Error)
	_, err = q.ConsumeWebAuthnSession(ctx, expired.ID, "login")
	require.Error(t, err)
	require.NoError(t, q.DeleteExpiredWebAuthnSessions(ctx))
}
