// Package authctx carries the authenticated user through request contexts and
// holds the small shared pieces of the session-token scheme (cookie name,
// token generation/hashing). It exists as its own tiny package so both the
// server middleware and the controllers can use it without import cycles.
package authctx

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"agent-orchestrator/db"
)

// CookieName is the short-lived access-session cookie. httpOnly + SameSite=Lax;
// the value is an opaque random token whose SHA-256 is stored in the sessions
// table.
const CookieName = "hc1_session"

// RefreshCookieName is the rotating refresh-token cookie. httpOnly and scoped to
// RefreshCookiePath so it is only ever sent to the refresh endpoint, shrinking
// its exposure. Its SHA-256 is stored in the refresh_tokens table.
const RefreshCookieName = "hc1_refresh"

// RefreshCookiePath narrows the refresh cookie to the one endpoint that needs
// it. Kept in sync with the route mounted under /api.
const RefreshCookiePath = "/api/auth/refresh"

// CSRFCookieName is the double-submit CSRF token. Deliberately NOT httpOnly so
// the frontend can read it and echo it back in the X-CSRF-Token header on
// mutating requests; a cross-site page can send the cookie but cannot read it.
const CSRFCookieName = "hc1_csrf"

type ctxKey struct{}

// WithUser returns a context carrying the authenticated user.
func WithUser(ctx context.Context, u db.User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// UserFrom returns the authenticated user, if any.
func UserFrom(ctx context.Context) (db.User, bool) {
	u, ok := ctx.Value(ctxKey{}).(db.User)
	return u, ok
}

// UserID returns the authenticated user's ID, or 0 when unauthenticated.
// Handlers behind RequireAuth can rely on a non-zero value.
func UserID(ctx context.Context) int32 {
	u, _ := UserFrom(ctx)
	return u.ID
}

// NewToken returns a fresh opaque session/reset token (256 bits, base64url).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken maps a raw token to the value stored in the database.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
