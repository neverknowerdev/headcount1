package endpoints

import (
	"agent-orchestrator/db/migrations"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAuthTestDB(t *testing.T) (*gorm.DB, *API) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	return database, NewAPI(database, nil, nil)
}

// TestRecoverConfirmRevokesRefreshTokens covers the A6 fix: account recovery is
// a full reset, so a pre-existing (possibly stolen) refresh token must not
// survive it.
func TestRecoverConfirmRevokesRefreshTokens(t *testing.T) {
	database, api := setupAuthTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	user, err := q.CreateUser(ctx, "recover@corp.io")
	require.NoError(t, err)

	// A live refresh token and an emailed reset token.
	refreshRaw, _ := authctx.NewToken()
	_, err = q.CreateRefreshToken(ctx, user.ID, "fam-1", authctx.HashToken(refreshRaw), time.Now().Add(time.Hour))
	require.NoError(t, err)
	resetRaw, _ := authctx.NewToken()
	require.NoError(t, database.Create(&db.PasswordResetToken{
		UserID: user.ID, TokenHash: authctx.HashToken(resetRaw), ExpiresAt: time.Now().Add(time.Hour),
	}).Error)

	body, _ := json.Marshal(map[string]string{"token": resetRaw})
	req := httptest.NewRequest(http.MethodPost, "/auth/recover/confirm", bytes.NewReader(body))
	w := httptest.NewRecorder()
	api.RecoverConfirm(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// The stolen refresh token is dead: rotating it must not mint a new pair.
	newRaw, _ := authctx.NewToken()
	_, err = q.RotateRefreshToken(ctx, authctx.HashToken(refreshRaw), authctx.HashToken(newRaw))
	require.Error(t, err, "a refresh token must not survive account recovery")
}

// TestAcceptTeamInviteFor covers the A4 fix: an invite is email-bound, so it may
// only be accepted by an account whose email matches the invited address.
func TestAcceptTeamInviteFor(t *testing.T) {
	database, api := setupAuthTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	owner, err := q.CreateUser(ctx, "owner@corp.io")
	require.NoError(t, err)
	require.NoError(t, q.EnsureTeamForUser(ctx, owner))
	ownerMembership, err := q.GetTeamMembership(ctx, owner.ID)
	require.NoError(t, err)

	// An invite issued to invited@corp.io.
	raw, _ := authctx.NewToken()
	require.NoError(t, database.Create(&db.TeamInvite{
		TeamID: ownerMembership.TeamID, Email: "invited@corp.io",
		TokenHash: authctx.HashToken(raw), ExpiresAt: time.Now().Add(time.Hour),
	}).Error)

	// A mismatched account cannot consume it.
	attacker, err := q.CreateUser(ctx, "attacker@evil.com")
	require.NoError(t, err)
	require.False(t, api.acceptTeamInviteFor(ctx, raw, attacker),
		"an invite must not be accepted under a different email")

	// The invite is still open (not burned by the failed attempt).
	invited, err := q.CreateUser(ctx, "invited@corp.io")
	require.NoError(t, err)
	require.True(t, api.acceptTeamInviteFor(ctx, raw, invited),
		"the invited email must be able to accept")
	require.True(t, q.IsTeamMember(ctx, ownerMembership.TeamID, invited.ID))
}

// TestRegisterBeginReEnroll covers the A7 fix: a credential-less account (post
// recovery, or an abandoned registration) may re-enroll; an account that still
// holds a passkey is a real duplicate and is rejected.
func TestRegisterBeginReEnroll(t *testing.T) {
	database, api := setupAuthTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	begin := func(email string) int {
		body, _ := json.Marshal(map[string]string{"email": email})
		req := httptest.NewRequest(http.MethodPost, "/auth/register/begin", bytes.NewReader(body))
		w := httptest.NewRecorder()
		api.RegisterBegin(w, req)
		return w.Code
	}

	// Brand-new email → ceremony starts.
	require.Equal(t, http.StatusOK, begin("fresh@corp.io"))

	// A credential-less existing account → re-enroll allowed.
	shredded, err := q.CreateUser(ctx, "shredded@corp.io")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, begin("shredded@corp.io"),
		"a credential-less account must be allowed to re-enroll")

	// An account WITH a passkey → 409 (genuine duplicate).
	_, err = q.CreateWebAuthnCredential(ctx, db.WebAuthnCredential{
		UserID: shredded.ID, CredentialID: []byte("c1"), PublicKey: []byte("p"),
		WrappedDEK: "w", PRFSalt: []byte("s"),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, begin("shredded@corp.io"),
		"an account that still has a passkey must be rejected as a duplicate")
}
