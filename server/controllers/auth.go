package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/secrets"
	"agent-orchestrator/pkg/utils"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const bcryptCost = 12

// dummyHash is compared against when a login email doesn't exist, so the
// request takes the same time as a real bcrypt check (no user enumeration
// via timing).
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("headcount1-dummy-password"), bcryptCost)

type userResponse struct {
	ID    int32  `json:"id"`
	Email string `json:"email"`
}

// ── register / login / logout / me ──────────────────────────────────────────

func (api *API) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	email := db.NormalizeEmail(req.Email)
	if !looksLikeEmail(email) {
		api.respondError(w, http.StatusBadRequest, "a valid email address is required")
		return
	}
	if len(req.Password) < 8 {
		api.respondError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := api.q.CreateUser(r.Context(), email, string(hash))
	if err != nil {
		// The unique index on email is the source of truth; treat any
		// constraint failure as "already registered".
		api.respondError(w, http.StatusConflict, "an account with this email already exists")
		return
	}
	if err := api.onUserCreated(r.Context(), user, req.Password); err != nil {
		log.Printf("auth: post-registration setup for %s failed: %v", user.Email, err)
	}
	if err := api.startSession(w, r, user); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusCreated, userResponse{ID: user.ID, Email: user.Email})
}

// onUserCreated runs per-user provisioning after an account is created
// (per-user encryption keys, builtin provider seeding). Failures are logged,
// not fatal — the account itself is already usable.
func (api *API) onUserCreated(ctx context.Context, user db.User, password string) error {
	if err := api.q.EnsureBuiltinLLMProvidersForUser(ctx, user.ID); err != nil {
		log.Printf("auth: seeding builtin providers for %s failed: %v", user.Email, err)
	}
	if err := api.q.EnsureDefaultModelSettingsForUser(ctx, user.ID); err != nil {
		log.Printf("auth: seeding default model settings for %s failed: %v", user.Email, err)
	}
	return secrets.Default().EnsureUserKey(user.ID, password)
}

func (api *API) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	user, err := api.q.GetUserByEmail(r.Context(), req.Email)
	storedHash := string(dummyHash)
	if err == nil {
		storedHash = user.PasswordHash
	}
	compareErr := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(req.Password))
	if err != nil || compareErr != nil {
		api.respondError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	api.syncUserKeyOnLogin(user, req.Password)
	if err := api.startSession(w, r, user); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, userResponse{ID: user.ID, Email: user.Email})
}

// syncUserKeyOnLogin keeps the user's encryption keyring consistent with the
// password that just authenticated: creates the key for accounts that predate
// per-user keys, and re-wraps a stale password wrap (possible after a crash
// between password update and re-wrap). Best-effort — login never fails on it.
func (api *API) syncUserKeyOnLogin(user db.User, password string) {
	store := secrets.Default()
	if err := store.EnsureUserKey(user.ID, password); err != nil {
		log.Printf("auth: ensuring encryption key for %s failed: %v", user.Email, err)
		return
	}
	if !store.VerifyUserPassword(user.ID, password) {
		log.Printf("auth: password wrap for %s is stale, re-wrapping", user.Email)
		if err := store.RewrapUserPassword(user.ID, password); err != nil {
			log.Printf("auth: re-wrap for %s failed: %v", user.Email, err)
		}
	}
}

// ChangePassword lets an authenticated user rotate their password. The
// encryption keyring's password wrap is re-created and every session is
// revoked (a fresh one is issued to this client).
func (api *API) ChangePassword(w http.ResponseWriter, r *http.Request) {
	user, ok := api.authenticate(r)
	if !ok {
		api.respondError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)) != nil {
		api.respondError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if len(req.NewPassword) < 8 {
		api.respondError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcryptCost)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := api.q.UpdateUserPassword(r.Context(), user.ID, string(hash)); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := secrets.Default().RewrapUserPassword(user.ID, req.NewPassword); err != nil {
		// The bcrypt hash is already rotated; the wrap heals on next login.
		log.Printf("auth: password-wrap rotation for %s failed: %v", user.Email, err)
	}
	if err := api.q.DeleteSessionsForUser(r.Context(), user.ID); err != nil {
		log.Printf("auth: session revocation for %s failed: %v", user.Email, err)
	}
	if err := api.startSession(w, r, user); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (api *API) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(authctx.CookieName); err == nil && c.Value != "" {
		_ = api.q.DeleteSessionByTokenHash(r.Context(), authctx.HashToken(c.Value))
	}
	clearSessionCookie(w, r)
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Me is the frontend AuthGate's probe: 200 + user when a session (or the E2E
// bypass) authenticates the request, 401 otherwise.
func (api *API) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := api.authenticate(r)
	if !ok {
		api.respondError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	api.respondJSON(w, http.StatusOK, userResponse{ID: user.ID, Email: user.Email})
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
// E2E fallback: test builds (utils.IsE2E) auto-login a fixture user so the
// browser e2e suite and local integration flows need no auth plumbing.
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

func (api *API) e2eUser(ctx context.Context) (db.User, error) {
	e2eUserMu.Lock()
	defer e2eUserMu.Unlock()
	user, err := api.q.GetUserByEmail(ctx, e2eUserEmail)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return db.User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("e2e-password"), bcrypt.MinCost)
	if err != nil {
		return db.User{}, err
	}
	user, err = api.q.CreateUser(ctx, e2eUserEmail, string(hash))
	if err != nil {
		return db.User{}, err
	}
	if err := api.onUserCreated(ctx, user, "e2e-password"); err != nil {
		log.Printf("auth: e2e fixture user setup failed: %v", err)
	}
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
