package db_test

import (
	"agent-orchestrator/db/migrations"
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
)

func setupSessionTestDB(t *testing.T) (*db.Queries, *gorm.DB) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	return db.New(database), database
}

// TestSessionAbsoluteCapRejectsPastCeiling verifies an access session is dead
// once its absolute ceiling passes, even while its sliding window is open — so
// a directly-used (never-refreshed) access cookie cannot renew forever.
func TestSessionAbsoluteCapRejectsPastCeiling(t *testing.T) {
	q, database := setupSessionTestDB(t)
	ctx := context.Background()
	user, err := q.CreateUser(ctx, "cap@example.com")
	require.NoError(t, err)

	raw, _ := authctx.NewToken()
	hash := authctx.HashToken(raw)

	// Absolute ceiling already in the past; the 1h sliding window is still open.
	_, err = q.CreateSession(ctx, user.ID, hash, time.Now().Add(-time.Minute))
	require.NoError(t, err)

	_, err = q.GetSessionUser(ctx, hash)
	require.Error(t, err, "session past its absolute ceiling must be rejected")

	// And it is deleted, not left lingering.
	var count int64
	require.NoError(t, database.Model(&db.Session{}).Where("token_hash = ?", hash).Count(&count).Error)
	require.Zero(t, count, "expired-by-cap session should be deleted")
}

// TestSessionWithinCapResolves confirms a normal session still works.
func TestSessionWithinCapResolves(t *testing.T) {
	q, _ := setupSessionTestDB(t)
	ctx := context.Background()
	user, err := q.CreateUser(ctx, "ok@example.com")
	require.NoError(t, err)

	raw, _ := authctx.NewToken()
	hash := authctx.HashToken(raw)
	_, err = q.CreateSession(ctx, user.ID, hash, time.Now().Add(db.SessionAbsoluteCap()))
	require.NoError(t, err)

	got, err := q.GetSessionUser(ctx, hash)
	require.NoError(t, err)
	require.Equal(t, user.ID, got.ID)
}
