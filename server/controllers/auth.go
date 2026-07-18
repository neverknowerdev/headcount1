package endpoints

import (
	"context"
	"crypto/sha256"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/mailer"
	"agent-orchestrator/pkg/secrets"
	"agent-orchestrator/pkg/utils"

	"gorm.io/gorm"
)

// Authentication is passwordless — see auth_webauthn.go for the passkey
// ceremonies. This file holds the session-cookie plumbing, the RequireAuth
// middleware, logout/me, the E2E bypass, and the mailer used by recovery.

type userResponse struct {
	ID    int32  `json:"id"`
	Email string `json:"email"`
}

// ── mailer (used by passkey recovery) ────────────────────────────────────────

// apiMailer sends recovery emails. Package-level (with a logging fallback) so
// main.go injects the SMTP configuration without threading it through NewAPI.
var apiMailer mailer.Mailer = mailer.NopMailer{}

// SetMailer installs the transactional mailer (called once from main).
func SetMailer(m mailer.Mailer) {
	if m != nil {
		apiMailer = m
	}
}

// appBaseURL is the public URL recovery links point at: APP_BASE_URL when set
// (cloud, behind a proxy), otherwise reconstructed from the request.
func appBaseURL(r *http.Request) string {
	if base := os.Getenv("APP_BASE_URL"); base != "" {
		return strings.TrimRight(base, "/")
	}
	scheme := "http"
	if requestIsTLS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ── logout / me ──────────────────────────────────────────────────────────────

func (api *API) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(authctx.CookieName); err == nil && c.Value != "" {
		hash := authctx.HashToken(c.Value)
		// Evict the user's DEK so their secrets are undecryptable once logged
		// out (resolve the user from the cookie — this is a public route).
		if user, err := api.q.GetSessionUser(r.Context(), hash); err == nil {
			secrets.LockUser(user.ID)
		}
		_ = api.q.DeleteSessionByTokenHash(r.Context(), hash)
	}
	clearSessionCookie(w, r)
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Me is the frontend AuthGate's probe: 200 + user (+ whether the vault is
// unlocked) when authenticated, 401 otherwise. The `locked` flag drives the
// AuthGate's re-tap unlock prompt after a crash.
func (api *API) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := api.authenticate(r)
	if !ok {
		api.respondError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]any{
		"id":     user.ID,
		"email":  user.Email,
		"locked": !secrets.IsUnlocked(user.ID),
	})
}

// ── middleware ───────────────────────────────────────────────────────────────

// RequireAuth gates the human-facing API: it resolves the session cookie to a
// user and stores it in the request context, or replies 401. The machine
// routes (LLM gateway proxy) and the public routes (/auth/*, /setup-status)
// are mounted outside this middleware — see main.go.
func (api *API) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := api.authenticate(r)
		if !ok {
			api.respondError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		next.ServeHTTP(w, r.WithContext(authctx.WithUser(r.Context(), user)))
	})
}

// authenticate resolves the request to a user via the session cookie, with an
// E2E fallback: test builds (utils.IsE2E) auto-login and auto-unlock a fixture
// user so the browser e2e suite and local integration flows need no auth or
// passkey plumbing.
func (api *API) authenticate(r *http.Request) (db.User, bool) {
	if c, err := r.Cookie(authctx.CookieName); err == nil && c.Value != "" {
		if user, err := api.q.GetSessionUser(r.Context(), authctx.HashToken(c.Value)); err == nil {
			return user, true
		}
	}
	if utils.IsE2E() {
		if user, err := api.e2eUser(r.Context()); err == nil {
			return user, true
		}
	}
	return db.User{}, false
}

const e2eUserEmail = "e2e@local"

// e2eUserMu serializes fixture-user creation: the e2e suite fires parallel
// requests at a fresh DB, and SQLite reports races as constraint errors.
var e2eUserMu sync.Mutex

// e2eDEK derives a deterministic data key for the fixture user, so E2E can
// seal/open secrets without a real passkey ceremony. Never used outside E2E.
func e2eDEK() [32]byte { return sha256.Sum256([]byte("headcount1-e2e-dek-v1")) }

func (api *API) e2eUser(ctx context.Context) (db.User, error) {
	e2eUserMu.Lock()
	defer e2eUserMu.Unlock()
	user, err := api.q.GetUserByEmail(ctx, e2eUserEmail)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		user, err = api.q.CreateUser(ctx, e2eUserEmail)
		if err != nil {
			return db.User{}, err
		}
		if err := api.q.EnsureTeamForUser(ctx, user); err != nil {
			log.Printf("auth: e2e fixture team setup failed: %v", err)
		}
		api.seedNewUser(ctx, user.ID)
	} else if err != nil {
		return db.User{}, err
	}
	// Always (re-)unlock so every E2E request can decrypt this user's secrets.
	secrets.UnlockUser(user.ID, e2eDEK(), keyringTTL)
	return user, nil
}

// ── session cookie plumbing ──────────────────────────────────────────────────

func (api *API) startSession(w http.ResponseWriter, r *http.Request, user db.User) error {
	token, err := authctx.NewToken()
	if err != nil {
		return err
	}
	if _, err := api.q.CreateSession(r.Context(), user.ID, authctx.HashToken(token)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authctx.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsTLS(r),
		MaxAge:   int(db.SessionLifetime / time.Second),
	})
	return nil
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     authctx.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsTLS(r),
		MaxAge:   -1,
	})
}

func requestIsTLS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func looksLikeEmail(email string) bool {
	at := strings.Index(email, "@")
	return len(email) >= 3 && len(email) <= 254 &&
		at > 0 && at < len(email)-1 && !strings.ContainsAny(email, " \t\n")
}
