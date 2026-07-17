package integration

import (
	"context"
	"net/http"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/runtokens"
)

// gatewayPrincipal is who authenticated a gateway request: an agent run (via
// an engine-issued run token) or a logged-in user (via the session cookie).
// Exactly one field is set.
type gatewayPrincipal struct {
	runID  int32
	userID int32
}

type principalCtxKey struct{}

func principalFrom(ctx context.Context) gatewayPrincipal {
	p, _ := ctx.Value(principalCtxKey{}).(gatewayPrincipal)
	return p
}

// SetRunTokenValidator switches the gateway from open (local/legacy/tests)
// to enforced mode: every machine-route request must present a valid run
// token or an authenticated session. Wired once from main.go.
func (g *LLMGateway) SetRunTokenValidator(validate func(token string) (int32, bool)) {
	g.validateRunToken = validate
}

// requireGatewayAuth gates the machine routes. Three outcomes:
//   - engine-issued run token → run principal; an X-Run-ID header, if present,
//     must name the same run (a token for run A can't write into run B's log);
//   - session cookie → user principal; handlers additionally verify the user
//     may use the addressed provider/agent/group (the URLs are shown in the
//     UI as OpenAI-compatible endpoints, so same-browser use keeps working);
//   - neither → 401.
//
// With no validator installed the gateway stays open — the pre-cloud
// behavior, kept for unit tests and stripped-down embeddings.
func (g *LLMGateway) requireGatewayAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.validateRunToken == nil {
			next.ServeHTTP(w, r)
			return
		}

		if token := r.Header.Get(runtokens.TokenHeader); token != "" {
			runID, ok := g.validateRunToken(token)
			if !ok {
				http.Error(w, "invalid or expired run token", http.StatusUnauthorized)
				return
			}
			if claimed := parseRunID(r); claimed != 0 && int32(claimed) != runID {
				http.Error(w, "X-Run-ID does not match the run token", http.StatusForbidden)
				return
			}
			ctx := context.WithValue(r.Context(), principalCtxKey{}, gatewayPrincipal{runID: runID})
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if c, err := r.Cookie(authctx.CookieName); err == nil && c.Value != "" {
			if user, err := g.q.GetSessionUser(r.Context(), authctx.HashToken(c.Value)); err == nil {
				ctx := context.WithValue(r.Context(), principalCtxKey{}, gatewayPrincipal{userID: user.ID})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		http.Error(w, "gateway requires a run token or an authenticated session", http.StatusUnauthorized)
		return
	})
}

// ── per-resource checks for session principals ───────────────────────────────
//
// Run-token principals skip these: the engine resolved the target through
// tenant-scoped data before issuing the token. Session principals address
// resources by client-supplied IDs, so ownership must be verified. A zero
// principal means enforcement is off.

func (g *LLMGateway) sessionMayUseProvider(r *http.Request, provider db.LLMProvider) bool {
	p := principalFrom(r.Context())
	if p.userID == 0 {
		return true // run token or enforcement off
	}
	return provider.UserID != nil && *provider.UserID == p.userID
}

func (g *LLMGateway) sessionMayUseGroup(r *http.Request, group db.ModelGroup) bool {
	p := principalFrom(r.Context())
	if p.userID == 0 {
		return true
	}
	return group.UserID != nil && *group.UserID == p.userID
}

func (g *LLMGateway) sessionMayUseAgent(r *http.Request, agent db.Agent) bool {
	p := principalFrom(r.Context())
	if p.userID == 0 {
		return true
	}
	company, err := g.q.GetCompany(r.Context(), agent.CompanyID)
	if err != nil {
		return false
	}
	if company.TeamID != nil {
		return g.q.IsTeamMember(r.Context(), *company.TeamID, p.userID)
	}
	return company.UserID != nil && *company.UserID == p.userID
}
