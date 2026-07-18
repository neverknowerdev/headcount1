package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	endpoints "agent-orchestrator/server/controllers"
)

func setupTokenTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(
		&db.User{}, &db.Session{}, &db.RefreshToken{},
	))
	return database
}

// findCookie returns the named Set-Cookie from a response, or nil.
func findCookie(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestRefreshRotatesTokenPair(t *testing.T) {
	database := setupTokenTestDB(t)
	q := db.New(database)
	ctx := context.Background()
	api := endpoints.NewAPI(database, nil, nil)

	user, err := q.CreateUser(ctx, "rotate@test.local")
	require.NoError(t, err)

	// Seed a live refresh token (as issueTokenPair would).
	raw, _ := authctx.NewToken()
	_, err = q.CreateRefreshToken(ctx, user.ID, "fam-1", authctx.HashToken(raw), time.Now().Add(db.SessionAbsoluteCap()))
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Post("/auth/refresh", api.Refresh)

	// Present the refresh cookie → new access + rotated refresh + csrf.
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: authctx.RefreshCookieName, Value: raw})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	res := w.Result()
	require.NotNil(t, findCookie(res, authctx.CookieName), "access cookie set")
	require.NotNil(t, findCookie(res, authctx.CSRFCookieName), "csrf cookie set")
	rotated := findCookie(res, authctx.RefreshCookieName)
	require.NotNil(t, rotated, "refresh cookie rotated")
	require.NotEqual(t, raw, rotated.Value, "refresh token value must change on rotation")

	// The new access cookie authenticates.
	access := findCookie(res, authctx.CookieName)
	_, err = q.GetSessionUser(ctx, authctx.HashToken(access.Value))
	require.NoError(t, err)

	// Age the original token past the concurrency grace window so replaying it
	// reads as theft, not a benign race.
	require.NoError(t, database.Model(&db.RefreshToken{}).Where("token_hash = ?", authctx.HashToken(raw)).
		Update("used_at", time.Now().Add(-time.Hour)).Error)

	// Replaying the ORIGINAL refresh token now trips reuse-detection → 401.
	req2 := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req2.AddCookie(&http.Cookie{Name: authctx.RefreshCookieName, Value: raw})
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusUnauthorized, w2.Code)

	// And the family is now burned: even the rotated token is dead.
	req3 := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req3.AddCookie(&http.Cookie{Name: authctx.RefreshCookieName, Value: rotated.Value})
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	require.Equal(t, http.StatusUnauthorized, w3.Code, "reuse must revoke the whole family")
}

func TestRefreshWithoutCookieIs401(t *testing.T) {
	database := setupTokenTestDB(t)
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Post("/auth/refresh", api.Refresh)

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestCSRFDoubleSubmit(t *testing.T) {
	database := setupTokenTestDB(t)
	q := db.New(database)
	ctx := context.Background()
	api := endpoints.NewAPI(database, nil, nil)

	user, err := q.CreateUser(ctx, "csrf@test.local")
	require.NoError(t, err)
	raw, _ := authctx.NewToken()
	_, err = q.CreateSession(ctx, user.ID, authctx.HashToken(raw))
	require.NoError(t, err)
	sessionCookie := &http.Cookie{Name: authctx.CookieName, Value: raw}

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(api.RequireAuth)
		r.Use(api.CSRF)
		r.Post("/mutate", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		r.Get("/read", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	csrf := "csrf-token-value"
	csrfCookie := &http.Cookie{Name: authctx.CSRFCookieName, Value: csrf}

	// Mutation WITH csrf cookie but no header → rejected.
	req := httptest.NewRequest(http.MethodPost, "/mutate", nil)
	req.AddCookie(sessionCookie)
	req.AddCookie(csrfCookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code, "missing X-CSRF-Token must be rejected")

	// Mutation WITH matching header → allowed.
	req = httptest.NewRequest(http.MethodPost, "/mutate", nil)
	req.AddCookie(sessionCookie)
	req.AddCookie(csrfCookie)
	req.Header.Set("X-CSRF-Token", csrf)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "matching token must pass")

	// Mutation with mismatched header → rejected.
	req = httptest.NewRequest(http.MethodPost, "/mutate", nil)
	req.AddCookie(sessionCookie)
	req.AddCookie(csrfCookie)
	req.Header.Set("X-CSRF-Token", "wrong")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)

	// Non-browser client (no csrf cookie) is exempt — bare session works.
	req = httptest.NewRequest(http.MethodPost, "/mutate", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "cookieless machine clients are not CSRF-gated")

	// Safe methods are never gated, even with a csrf cookie and no header.
	req = httptest.NewRequest(http.MethodGet, "/read", nil)
	req.AddCookie(sessionCookie)
	req.AddCookie(csrfCookie)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestMeReportsSessionTiming(t *testing.T) {
	database := setupTokenTestDB(t)
	q := db.New(database)
	ctx := context.Background()
	api := endpoints.NewAPI(database, nil, nil)

	user, err := q.CreateUser(ctx, "timing@test.local")
	require.NoError(t, err)

	// A live access session + a refresh family whose ceiling is a full cap out.
	raw, _ := authctx.NewToken()
	_, err = q.CreateSession(ctx, user.ID, authctx.HashToken(raw))
	require.NoError(t, err)
	rraw, _ := authctx.NewToken()
	ceiling := time.Now().Add(db.SessionAbsoluteCap())
	_, err = q.CreateRefreshToken(ctx, user.ID, "fam-1", authctx.HashToken(rraw), ceiling)
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Get("/auth/me", api.Me)

	call := func() map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: authctx.CookieName, Value: raw})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		return body
	}

	// Fresh login: timing is reported and re-auth is not yet due.
	body := call()
	require.Contains(t, body, "session_expires_at")
	require.Contains(t, body, "reauth_at")
	require.Equal(t, false, body["reauth_required"])
	exp, err := time.Parse(time.RFC3339, body["session_expires_at"].(string))
	require.NoError(t, err)
	reauthAt, err := time.Parse(time.RFC3339, body["reauth_at"].(string))
	require.NoError(t, err)
	require.WithinDuration(t, ceiling, exp, 2*time.Second)
	require.Equal(t, db.SessionReauthGap(), exp.Sub(reauthAt), "reauth deadline is exactly gap before the ceiling")

	// Move the ceiling into the re-auth window (< gap remaining) → required flips.
	near := time.Now().Add(db.SessionReauthGap() / 2)
	require.NoError(t, database.Model(&db.RefreshToken{}).
		Where("family_id = ?", "fam-1").Update("absolute_expires_at", near).Error)
	require.Equal(t, true, call()["reauth_required"])
}
