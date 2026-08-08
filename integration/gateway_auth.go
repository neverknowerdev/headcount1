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
				// A session user may name a run via X-Run-ID (logging/accounting),
				// so it must belong to their tenant — otherwise B could inject into
				// A's run log. Mirrors the run-token X-Run-ID guard above.
				if claimed := parseRunID(r); claimed != 0 && !g.userMayUseRun(r.Context(), user.ID, int32(claimed)) {
					http.Error(w, "X-Run-ID does not belong to you", http.StatusForbidden)
					return
				}
				ctx := context.WithValue(r.Context(), principalCtxKey{}, gatewayPrincipal{userID: user.ID})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		http.Error(w, "gateway requires a run token or an authenticated session", http.StatusUnauthorized)
		return
	})
}

// ── per-resource authorization ───────────────────────────────────────────────
//
// Every gateway request is bound to a tenant, whether it authenticated with a
// session cookie (a user) or a run token (an agent run owned by a company). The
// addressed resource is resolved from a client-supplied, globally-unique id/slug
// (`X-Provider-ID`, `/proxy/group/{slug}`, `/proxy/agent/{id}`), so it must be
// re-checked against the principal's tenant here — an issuer-time check is not
// enough because the target is chosen by the client at request time. A zero
// principal (no field set) means enforcement is off (local/test).

// companyGrantsUser reports whether userID is within a company's tenant: a
// member of its owning team, or the creator of a team-less company.
func (g *LLMGateway) companyGrantsUser(ctx context.Context, company db.Company, userID int32) bool {
	if company.TeamID != nil {
		return g.q.IsTeamMember(ctx, *company.TeamID, userID)
	}
	return company.UserID != nil && *company.UserID == userID
}

// companyOfRun resolves the company that owns a run.
func (g *LLMGateway) companyOfRun(ctx context.Context, runID int32) (db.Company, bool) {
	_, task, err := g.q.GetRunWithTask(ctx, runID)
	if err != nil {
		return db.Company{}, false
	}
	company, err := g.q.GetCompany(ctx, task.CompanyID)
	if err != nil {
		return db.Company{}, false
	}
	return company, true
}

// userMayUseRun reports whether a run belongs to the user's tenant.
func (g *LLMGateway) userMayUseRun(ctx context.Context, userID, runID int32) bool {
	company, ok := g.companyOfRun(ctx, runID)
	return ok && g.companyGrantsUser(ctx, company, userID)
}

// userOwnedResourceAllowed authorizes a user-owned resource (provider/group,
// which carry a single owner UserID) for the current principal: the session
// user must own it; a run token's owning company/team must include that owner.
func (g *LLMGateway) userOwnedResourceAllowed(r *http.Request, ownerUserID *int32) bool {
	p := principalFrom(r.Context())
	switch {
	case p.userID != 0:
		return ownerUserID != nil && *ownerUserID == p.userID
	case p.runID != 0:
		// Ownerless (shared/builtin/local) resources are not tenant-scoped, so a
		// run may use them. A user-owned resource is only allowed when its owner
		// belongs to the run's company/team — this is what stops run A from
		// spending run B's owner's key via a client-supplied slug.
		if ownerUserID == nil {
			return true
		}
		company, ok := g.companyOfRun(r.Context(), p.runID)
		return ok && g.companyGrantsUser(r.Context(), company, *ownerUserID)
	default:
		return true // enforcement off
	}
}

func (g *LLMGateway) mayUseProvider(r *http.Request, provider db.LLMProvider) bool {
	// An ownerless, non-builtin provider that still carries a stored credential
	// is not a genuinely shared resource — the only ownerless providers meant to
	// be usable across tenants are the builtin catalog entries. Don't let an
	// arbitrary run spend a stray ownerless key. (Providers created via the API
	// always carry a UserID, so this only bites legacy/imported rows.)
	if p := principalFrom(r.Context()); p.runID != 0 &&
		provider.UserID == nil && !provider.Builtin && provider.ApiKeyEncrypted != "" {
		return false
	}
	return g.userOwnedResourceAllowed(r, provider.UserID)
}

func (g *LLMGateway) mayUseGroup(r *http.Request, group db.ModelGroup) bool {
	return g.userOwnedResourceAllowed(r, group.UserID)
}

func (g *LLMGateway) mayUseAgent(r *http.Request, agent db.Agent) bool {
	p := principalFrom(r.Context())
	switch {
	case p.userID != 0:
		company, err := g.q.GetCompany(r.Context(), agent.CompanyID)
		return err == nil && g.companyGrantsUser(r.Context(), company, p.userID)
	case p.runID != 0:
		// The addressed agent must belong to the same company as the run.
		company, ok := g.companyOfRun(r.Context(), p.runID)
		return ok && company.ID == agent.CompanyID
	default:
		return true // enforcement off
	}
}
