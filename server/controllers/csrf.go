package endpoints

import (
	"crypto/subtle"
	"net/http"

	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/utils"
)

// CSRF is a double-submit-cookie guard for the authenticated, human-facing API.
// On a mutating request it requires the X-CSRF-Token header to match the
// non-httpOnly hc1_csrf cookie. A cross-site page can cause the browser to send
// the cookie but cannot read it (same-origin policy), so it cannot forge the
// matching header — the request is rejected.
//
// It is deliberately permissive in two cases so it protects real browsers
// without breaking machine/test clients:
//   - E2E mode: the fixture bypass authenticates cookielessly, so there is no
//     CSRF cookie to echo; skip entirely.
//   - No CSRF cookie present: non-browser callers (integration tests, tooling
//     driving the API with a bare session) never receive an hc1_csrf cookie, so
//     there is nothing to double-submit. A genuine logged-in browser always has
//     the cookie (issued at login/refresh), so enforcement always applies there.
func (api *API) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if utils.IsE2E() || csrfSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(authctx.CSRFCookieName)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		header := r.Header.Get("X-CSRF-Token")
		if header == "" || subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) != 1 {
			api.respondError(w, http.StatusForbidden, "csrf token mismatch")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func csrfSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
