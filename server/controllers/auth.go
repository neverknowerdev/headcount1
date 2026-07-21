package endpoints

import (
	"context"
	"crypto/sha256"
	"errors"
	"log"
	"net"
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

// appBaseURL is the public URL recovery/invite links point at. It prefers the
// operator-configured APP_BASE_URL. Falling back to the request's Host header is
// only safe when the link is NOT emailed to a third party: an attacker who knows
// a victim's email can send a request with a spoofed Host header and, if we
// built the link from it, the victim would receive a link to the attacker's
// domain that leaks the reset/invite token (CWE-640). So when a real mailer is
// configured we refuse the Host fallback and require APP_BASE_URL; with the
// dev/local NopMailer (which only logs the link) the Host reconstruction is
// retained for convenience.
func appBaseURL(r *http.Request) (string, error) {
	if base := os.Getenv("APP_BASE_URL"); base != "" {
		return strings.TrimRight(base, "/"), nil
	}
	if _, isNop := apiMailer.(mailer.NopMailer); !isNop {
		return "", errors.New("APP_BASE_URL is not set — refusing to build an email link from the untrusted request Host header")
	}
	scheme := "http"
	if requestIsTLS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host, nil
}

// ── logout / me ──────────────────────────────────────────────────────────────

func (api *API) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(authctx.CookieName); err == nil && c.Value != "" {
		hash := authctx.HashToken(c.Value)
		// Evict the user's DEK so their secrets are undecryptable once logged
		// out (resolve the user from the cookie — this is a public route), and
		// revoke every refresh token so the lineage cannot be resumed.
		if user, err := api.q.GetSessionUser(r.Context(), hash); err == nil {
			secrets.LockUser(user.ID)
			_ = api.q.RevokeRefreshTokensForUser(r.Context(), user.ID)
			// Logout already revokes every refresh lineage for the user; drop
			// their access sessions too so no other still-live access cookie
			// keeps working for up to an hour after an explicit logout.
			_ = api.q.DeleteSessionsForUser(r.Context(), user.ID)
		}
		_ = api.q.DeleteSessionByTokenHash(r.Context(), hash)
	}
	clearSessionCookie(w, r)
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Me is the frontend AuthGate's probe: 200 + user (+ whether the vault is
// unlocked) when authenticated, 401 otherwise. The `locked` flag drives the
// AuthGate's re-tap unlock prompt after a crash; the session-timing fields
// drive the proactive re-auth banner and the pre-expiry hard gate.
func (api *API) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := api.authenticate(r)
	if !ok {
		api.respondError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	api.respondJSON(w, http.StatusOK, api.authStateResponse(r.Context(), user))
}

// authStateResponse is the shared /auth/me and /auth/refresh body: identity,
// vault lock state, and — when the user has an active login — when that login's
// hard ceiling falls (session_expires_at) and when the UI should start nudging
// a passkey re-auth (reauth_at = ceiling − SessionReauthGap). Both are RFC3339
// UTC; the frontend drives everything off a local clock, so no polling.
func (api *API) authStateResponse(ctx context.Context, user db.User) map[string]any {
	resp := map[string]any{
		"id":     user.ID,
		"email":  user.Email,
		"locked": !secrets.IsUnlocked(user.ID),
		// role drives the frontend hiding of owner-only actions (create/delete
		// company & project, delete MCP server). "owner" for solo users and team
		// creators; "member" for invited users. The backend enforces regardless.
		"role": api.teamRole(ctx, user.ID),
	}
	if exp, err := api.q.GetSessionAbsoluteExpiry(ctx, user.ID); err == nil && !exp.IsZero() {
		reauthAt := exp.Add(-db.SessionReauthGap())
		resp["session_expires_at"] = exp.UTC().Format(time.RFC3339)
		resp["reauth_at"] = reauthAt.UTC().Format(time.RFC3339)
		resp["reauth_required"] = !time.Now().Before(reauthAt)
	}
	return resp
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
	secrets.UnlockUser(user.ID, e2eDEK(), keyringTTL())
	return user, nil
}

// ── access/refresh token plumbing ────────────────────────────────────────────

// issueTokenPair establishes a fresh login: a short-lived access session, a
// rotating refresh token (new family), and a double-submit CSRF token. Called
// on register/login finish and by the E2E register bypass.
func (api *API) issueTokenPair(w http.ResponseWriter, r *http.Request, user db.User) error {
	// Access session (short; slides while active) and refresh family share one
	// absolute ceiling, so a directly-used access cookie can never outlive the
	// login's hard cap.
	absExpiry := time.Now().Add(db.SessionAbsoluteCap())
	access, err := authctx.NewToken()
	if err != nil {
		return err
	}
	if _, err := api.q.CreateSession(r.Context(), user.ID, authctx.HashToken(access), absExpiry); err != nil {
		return err
	}
	setAccessCookie(w, r, access)

	// Refresh token: first member of a new family, capped by the same ceiling.
	if err := api.issueRefreshToken(w, r, user.ID, "", absExpiry); err != nil {
		return err
	}
	setCSRFCookie(w, r)
	return nil
}

// issueRefreshToken mints a refresh token and sets its cookie. familyID=="" starts
// a new family (fresh login); a non-empty familyID continues an existing lineage
// (rotation) — but rotation goes through RotateRefreshToken, so callers here pass
// "" and let CreateRefreshToken assign a fresh family.
func (api *API) issueRefreshToken(w http.ResponseWriter, r *http.Request, userID int32, familyID string, absExpiry time.Time) error {
	refresh, err := authctx.NewToken()
	if err != nil {
		return err
	}
	if familyID == "" {
		familyID, err = authctx.NewToken()
		if err != nil {
			return err
		}
	}
	if _, err := api.q.CreateRefreshToken(r.Context(), userID, familyID, authctx.HashToken(refresh), absExpiry); err != nil {
		return err
	}
	setRefreshCookie(w, r, refresh)
	return nil
}

// Refresh exchanges a valid refresh token for a new access+refresh pair,
// rotating the refresh token. Replaying a spent token trips reuse-detection,
// which revokes the whole family and forces a re-login. Path-scoped cookie +
// rotation keep a stolen token containable.
func (api *API) Refresh(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(authctx.RefreshCookieName)
	if err != nil || c.Value == "" {
		api.respondError(w, http.StatusUnauthorized, "no refresh token")
		return
	}
	newRefresh, err := authctx.NewToken()
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	next, err := api.q.RotateRefreshToken(r.Context(), authctx.HashToken(c.Value), authctx.HashToken(newRefresh))
	if err != nil {
		// Reuse detected or token invalid/expired — clear everything and 401.
		api.clearAuthCookies(w, r)
		api.respondError(w, http.StatusUnauthorized, "refresh rejected")
		return
	}
	// Mint a new access session and set the rotated refresh + CSRF cookies.
	access, err := authctx.NewToken()
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	// The new access session inherits the family's absolute ceiling, so
	// rotating near the cap can't mint a session that outlives it.
	if _, err := api.q.CreateSession(r.Context(), next.UserID, authctx.HashToken(access), next.AbsoluteExpiresAt); err != nil {
		api.respondError(w, http.StatusInternalServerError, "session creation failed")
		return
	}
	setAccessCookie(w, r, access)
	setRefreshCookie(w, r, newRefresh)
	setCSRFCookie(w, r)

	user, err := api.q.GetUser(r.Context(), next.UserID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "user lookup failed")
		return
	}
	api.respondJSON(w, http.StatusOK, api.authStateResponse(r.Context(), user))
}

func setAccessCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     authctx.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(r),
		MaxAge:   int(db.AccessTokenLifetime / time.Second),
	})
}

func setRefreshCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     authctx.RefreshCookieName,
		Value:    token,
		Path:     authctx.RefreshCookiePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(r),
		MaxAge:   int(db.SessionAbsoluteCap() / time.Second),
	})
}

// setCSRFCookie sets the double-submit token. Not httpOnly by design: the SPA
// reads it and echoes it in X-CSRF-Token on mutations.
func setCSRFCookie(w http.ResponseWriter, r *http.Request) {
	token, err := authctx.NewToken()
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authctx.CSRFCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(r),
		MaxAge:   int(db.SessionAbsoluteCap() / time.Second),
	})
}

// clearAuthCookies expires the access, refresh, and CSRF cookies.
func (api *API) clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w, r)
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	secure := cookieSecure(r)
	http.SetCookie(w, &http.Cookie{
		Name: authctx.CookieName, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure, MaxAge: -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name: authctx.RefreshCookieName, Value: "", Path: authctx.RefreshCookiePath,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure, MaxAge: -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name: authctx.CSRFCookieName, Value: "", Path: "/",
		HttpOnly: false, SameSite: http.SameSiteLaxMode, Secure: secure, MaxAge: -1,
	})
}

func requestIsTLS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// cookieSecure decides the Secure attribute for auth cookies. It defaults to
// true so that a reverse proxy which terminates TLS but forgets to set
// X-Forwarded-Proto can never cause a session/refresh/CSRF cookie to be issued
// over the clear. The only exception is a plain-HTTP request to a loopback host
// (local dev and E2E on http://localhost), where the browser would silently drop
// a Secure cookie.
func cookieSecure(r *http.Request) bool {
	if requestIsTLS(r) {
		return true
	}
	return !isLoopbackHost(r)
}

func isLoopbackHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func looksLikeEmail(email string) bool {
	at := strings.Index(email, "@")
	return len(email) >= 3 && len(email) <= 254 &&
		at > 0 && at < len(email)-1 && !strings.ContainsAny(email, " \t\n")
}
