package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	endpoints "agent-orchestrator/server/controllers"
)

func setupAuthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.Session{}, &db.PasswordResetToken{}))
	return database
}

func setupAuthRouter(database *gorm.DB) chi.Router {
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Post("/auth/register", api.Register)
	r.Post("/auth/login", api.Login)
	r.Post("/auth/logout", api.Logout)
	r.Get("/auth/me", api.Me)
	// A representative protected route.
	r.Group(func(r chi.Router) {
		r.Use(api.RequireAuth)
		r.Get("/protected", func(w http.ResponseWriter, r *http.Request) {
			u := authctx.UserID(r.Context())
			json.NewEncoder(w).Encode(map[string]int32{"user_id": u})
		})
	})
	return r
}

func postJSON(t *testing.T, r chi.Router, path string, body map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == authctx.CookieName && c.Value != "" {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func TestRegisterLoginLogoutFlow(t *testing.T) {
	database := setupAuthTestDB(t)
	r := setupAuthRouter(database)

	// Register sets a session cookie with the right attributes.
	w := postJSON(t, r, "/auth/register", map[string]string{"email": " Alice@Example.com ", "password": "hunter2secret"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	cookie := sessionCookie(t, w)
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	assert.Equal(t, "/", cookie.Path)

	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.Equal(t, "alice@example.com", created["email"], "email must be normalized")

	// The cookie authenticates protected routes and /auth/me.
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(cookie)
	pw := httptest.NewRecorder()
	r.ServeHTTP(pw, req)
	require.Equal(t, http.StatusOK, pw.Code)

	req = httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.AddCookie(cookie)
	mw := httptest.NewRecorder()
	r.ServeHTTP(mw, req)
	require.Equal(t, http.StatusOK, mw.Code)

	// No cookie → 401 on both.
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	uw := httptest.NewRecorder()
	r.ServeHTTP(uw, req)
	require.Equal(t, http.StatusUnauthorized, uw.Code)

	// Logout revokes the session server-side.
	lw := postJSON(t, r, "/auth/logout", nil, cookie)
	require.Equal(t, http.StatusOK, lw.Code)
	req = httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.AddCookie(cookie)
	dw := httptest.NewRecorder()
	r.ServeHTTP(dw, req)
	require.Equal(t, http.StatusUnauthorized, dw.Code, "revoked session must not authenticate")

	// Login works with the original (pre-normalization) email.
	lw2 := postJSON(t, r, "/auth/login", map[string]string{"email": "ALICE@example.COM", "password": "hunter2secret"})
	require.Equal(t, http.StatusOK, lw2.Code)
	sessionCookie(t, lw2)
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	database := setupAuthTestDB(t)
	r := setupAuthRouter(database)
	postJSON(t, r, "/auth/register", map[string]string{"email": "a@b.co", "password": "correct-horse"})

	w := postJSON(t, r, "/auth/login", map[string]string{"email": "a@b.co", "password": "wrong-password"})
	require.Equal(t, http.StatusUnauthorized, w.Code)
	w = postJSON(t, r, "/auth/login", map[string]string{"email": "nobody@b.co", "password": "whatever123"})
	require.Equal(t, http.StatusUnauthorized, w.Code)
	// Identical error body for unknown email vs wrong password (no enumeration).
	assert.Contains(t, w.Body.String(), "invalid email or password")
}

func TestRegisterValidation(t *testing.T) {
	database := setupAuthTestDB(t)
	r := setupAuthRouter(database)

	w := postJSON(t, r, "/auth/register", map[string]string{"email": "not-an-email", "password": "long-enough-pw"})
	require.Equal(t, http.StatusBadRequest, w.Code)
	w = postJSON(t, r, "/auth/register", map[string]string{"email": "a@b.co", "password": "short"})
	require.Equal(t, http.StatusBadRequest, w.Code)

	w = postJSON(t, r, "/auth/register", map[string]string{"email": "dup@b.co", "password": "long-enough-pw"})
	require.Equal(t, http.StatusCreated, w.Code)
	w = postJSON(t, r, "/auth/register", map[string]string{"email": "DUP@b.co", "password": "long-enough-pw"})
	require.Equal(t, http.StatusConflict, w.Code, "duplicate email (case-insensitive) must 409")
}

func TestExpiredSessionRejectedAndGCed(t *testing.T) {
	database := setupAuthTestDB(t)
	r := setupAuthRouter(database)
	q := db.New(database)

	w := postJSON(t, r, "/auth/register", map[string]string{"email": "a@b.co", "password": "long-enough-pw"})
	require.Equal(t, http.StatusCreated, w.Code)
	cookie := sessionCookie(t, w)

	// Force the session past its expiry.
	require.NoError(t, database.Model(&db.Session{}).
		Where("token_hash = ?", authctx.HashToken(cookie.Value)).
		Update("expires_at", time.Now().Add(-time.Minute)).Error)

	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.AddCookie(cookie)
	mw := httptest.NewRecorder()
	r.ServeHTTP(mw, req)
	require.Equal(t, http.StatusUnauthorized, mw.Code)

	require.NoError(t, q.DeleteExpiredSessions(context.Background()))
	var n int64
	database.Model(&db.Session{}).Count(&n)
	assert.Equal(t, int64(0), n)
}

func TestPasswordHashNeverSerialized(t *testing.T) {
	database := setupAuthTestDB(t)
	r := setupAuthRouter(database)
	w := postJSON(t, r, "/auth/register", map[string]string{"email": "a@b.co", "password": "long-enough-pw"})
	require.Equal(t, http.StatusCreated, w.Code)
	assert.NotContains(t, w.Body.String(), "password")

	var u db.User
	require.NoError(t, database.First(&u).Error)
	b, _ := json.Marshal(u)
	assert.NotContains(t, string(b), "password_hash")
}
