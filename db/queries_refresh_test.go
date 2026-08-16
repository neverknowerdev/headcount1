package db_test

import (
	"agent-orchestrator/db/migrations"
	"context"
	"testing"
	"time"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupRefreshTestDB(t *testing.T) (*db.Queries, *gorm.DB, context.Context, int32) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	q := db.New(database)
	ctx := context.Background()
	user, err := q.CreateUser(ctx, "refresh@test.local")
	require.NoError(t, err)
	return q, database, ctx, user.ID
}

func absExpiry() time.Time { return time.Now().Add(db.SessionAbsoluteCap()) }

func TestRotateRefreshTokenHappyPath(t *testing.T) {
	q, _, ctx, userID := setupRefreshTestDB(t)

	_, err := q.CreateRefreshToken(ctx, userID, "fam-1", "hash-1", absExpiry())
	require.NoError(t, err)

	// Rotate: old token is spent, a successor in the same family is minted.
	next, err := q.RotateRefreshToken(ctx, "hash-1", "hash-2")
	require.NoError(t, err)
	require.Equal(t, "fam-1", next.FamilyID)
	require.Equal(t, userID, next.UserID)
	require.Equal(t, "hash-2", next.TokenHash)

	// The successor rotates again — a normal continuing session.
	next2, err := q.RotateRefreshToken(ctx, "hash-2", "hash-3")
	require.NoError(t, err)
	require.Equal(t, "fam-1", next2.FamilyID)
}

func TestRotateRefreshTokenReuseRevokesFamily(t *testing.T) {
	q, database, ctx, userID := setupRefreshTestDB(t)

	_, err := q.CreateRefreshToken(ctx, userID, "fam-1", "hash-1", absExpiry())
	require.NoError(t, err)

	// Legit rotation: hash-1 → hash-2.
	_, err = q.RotateRefreshToken(ctx, "hash-1", "hash-2")
	require.NoError(t, err)

	// Age hash-1's used_at past the concurrency grace window so replaying it
	// reads as genuine theft rather than a benign race.
	require.NoError(t, database.Model(&db.RefreshToken{}).Where("token_hash = ?", "hash-1").
		Update("used_at", time.Now().Add(-time.Hour)).Error)

	// An attacker replays the already-spent hash-1 → reuse detected.
	_, err = q.RotateRefreshToken(ctx, "hash-1", "hash-attacker")
	require.ErrorIs(t, err, db.ErrRefreshReuse)

	// Reuse burned the whole family: the legit successor hash-2 is now dead too
	// (rejected — the endpoint maps any rotation failure to a forced re-login).
	_, err = q.RotateRefreshToken(ctx, "hash-2", "hash-3")
	require.Error(t, err, "family revocation must kill the victim's live token")
}

func TestRotateRefreshTokenConcurrentGrace(t *testing.T) {
	q, _, ctx, userID := setupRefreshTestDB(t)

	_, err := q.CreateRefreshToken(ctx, userID, "fam-1", "hash-1", absExpiry())
	require.NoError(t, err)

	// Tab A rotates hash-1 → hash-2.
	_, err = q.RotateRefreshToken(ctx, "hash-1", "hash-2")
	require.NoError(t, err)

	// Tab B, racing, replays hash-1 immediately (within the grace window). It
	// must NOT burn the family — it gets a fresh successor instead.
	nextB, err := q.RotateRefreshToken(ctx, "hash-1", "hash-2b")
	require.NoError(t, err, "a concurrent refresh within grace must not trip reuse")
	require.Equal(t, "fam-1", nextB.FamilyID)

	// Both successors remain live (family not revoked): each can rotate on.
	_, err = q.RotateRefreshToken(ctx, "hash-2", "hash-3")
	require.NoError(t, err)
	_, err = q.RotateRefreshToken(ctx, "hash-2b", "hash-3b")
	require.NoError(t, err)
}

func TestRotateRefreshTokenAbsoluteCap(t *testing.T) {
	q, _, ctx, userID := setupRefreshTestDB(t)

	// Family whose absolute ceiling is already in the past.
	_, err := q.CreateRefreshToken(ctx, userID, "fam-1", "hash-1", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	_, err = q.RotateRefreshToken(ctx, "hash-1", "hash-2")
	require.Error(t, err, "a token past the absolute cap cannot be rotated")
	require.NotErrorIs(t, err, db.ErrRefreshReuse)
}

func TestRotateRefreshTokenExpired(t *testing.T) {
	q, database, ctx, userID := setupRefreshTestDB(t)

	// Manually insert an already-expired (but not revoked/used) token.
	rt := db.RefreshToken{
		FamilyID: "fam-1", TokenHash: "hash-1", UserID: userID,
		ExpiresAt: time.Now().Add(-time.Minute), AbsoluteExpiresAt: absExpiry(),
	}
	require.NoError(t, database.Create(&rt).Error)

	_, err := q.RotateRefreshToken(ctx, "hash-1", "hash-2")
	require.Error(t, err, "an expired refresh token cannot be rotated")
}

func TestRotateRefreshTokenUnknown(t *testing.T) {
	q, _, ctx, _ := setupRefreshTestDB(t)
	_, err := q.RotateRefreshToken(ctx, "does-not-exist", "hash-2")
	require.Error(t, err)
}

func TestRevokeRefreshFamily(t *testing.T) {
	q, _, ctx, userID := setupRefreshTestDB(t)
	_, err := q.CreateRefreshToken(ctx, userID, "fam-1", "hash-1", absExpiry())
	require.NoError(t, err)
	require.NoError(t, q.RevokeRefreshFamily(ctx, "fam-1"))
	_, err = q.RotateRefreshToken(ctx, "hash-1", "hash-2")
	require.Error(t, err, "a revoked family's token cannot rotate")
}

func TestDeleteExpiredRefreshTokens(t *testing.T) {
	q, database, ctx, userID := setupRefreshTestDB(t)

	// One live, one past its absolute cap, one revoked.
	_, err := q.CreateRefreshToken(ctx, userID, "live", "h-live", absExpiry())
	require.NoError(t, err)
	require.NoError(t, database.Create(&db.RefreshToken{
		FamilyID: "old", TokenHash: "h-old", UserID: userID,
		ExpiresAt: time.Now().Add(-time.Hour), AbsoluteExpiresAt: time.Now().Add(-time.Minute),
	}).Error)
	revoked := time.Now()
	require.NoError(t, database.Create(&db.RefreshToken{
		FamilyID: "rev", TokenHash: "h-rev", UserID: userID,
		ExpiresAt: absExpiry(), AbsoluteExpiresAt: absExpiry(), RevokedAt: &revoked,
	}).Error)

	require.NoError(t, q.DeleteExpiredRefreshTokens(ctx))

	var remaining int64
	database.Model(&db.RefreshToken{}).Count(&remaining)
	require.Equal(t, int64(1), remaining, "only the live token should survive GC")
}

func TestSessionReauthGapClampedInsideCap(t *testing.T) {
	// Default: 4-day gap, well inside the default 14-day cap.
	t.Setenv("SESSION_ABSOLUTE_CAP", "")
	t.Setenv("SESSION_REAUTH_GAP", "")
	require.Equal(t, 4*24*time.Hour, db.SessionReauthGap())

	// An oversized gap is clamped to cap-1 so the soft deadline stays strictly
	// inside the session (a 7-day cap can never carry a 10-day gap).
	t.Setenv("SESSION_ABSOLUTE_CAP", "7")
	t.Setenv("SESSION_REAUTH_GAP", "10")
	require.Equal(t, 6*24*time.Hour, db.SessionReauthGap())

	// A custom, valid gap is honoured.
	t.Setenv("SESSION_ABSOLUTE_CAP", "14")
	t.Setenv("SESSION_REAUTH_GAP", "3")
	require.Equal(t, 3*24*time.Hour, db.SessionReauthGap())

	// The reauth deadline (cap − gap) always precedes the hard cap.
	require.Less(t, db.SessionReauthGap(), db.SessionAbsoluteCap())
}

func TestGetSessionAbsoluteExpiry(t *testing.T) {
	q, database, ctx, userID := setupRefreshTestDB(t)

	// No refresh session yet → zero time (nothing to report).
	exp, err := q.GetSessionAbsoluteExpiry(ctx, userID)
	require.NoError(t, err)
	require.True(t, exp.IsZero())

	// Two live families: the later ceiling (the most recent login) wins.
	early := time.Now().Add(48 * time.Hour)
	late := time.Now().Add(240 * time.Hour)
	require.NoError(t, database.Create(&db.RefreshToken{
		FamilyID: "early", TokenHash: "h-early", UserID: userID,
		ExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: early,
	}).Error)
	require.NoError(t, database.Create(&db.RefreshToken{
		FamilyID: "late", TokenHash: "h-late", UserID: userID,
		ExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: late,
	}).Error)

	exp, err = q.GetSessionAbsoluteExpiry(ctx, userID)
	require.NoError(t, err)
	require.WithinDuration(t, late, exp, time.Second)

	// Revoked and already-expired tokens are ignored.
	revoked := time.Now()
	require.NoError(t, database.Model(&db.RefreshToken{}).
		Where("family_id = ?", "late").Update("revoked_at", revoked).Error)
	exp, err = q.GetSessionAbsoluteExpiry(ctx, userID)
	require.NoError(t, err)
	require.WithinDuration(t, early, exp, time.Second, "revoked family must be skipped")
}
