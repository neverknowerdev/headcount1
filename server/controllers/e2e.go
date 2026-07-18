package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/secrets"
	"agent-orchestrator/pkg/utils"

	"github.com/go-chi/chi/v5"
)

// WipeDB clears all data from the database. Only available when E2E_MODE=true.
// Route registration is guarded by the env var in main.go.
func (api *API) WipeDB(w http.ResponseWriter, r *http.Request) {
	if !utils.IsE2E() {
		http.Error(w, "WipeDB is only available in E2E mode", http.StatusForbidden)
		return
	}

	// Order matters: foreign keys are enforced, so every child is deleted
	// before its parent. The identity graph (sessions, keys, memberships,
	// teams, users) is wiped LAST — companies, providers, model groups and
	// default-model settings all reference users/teams and must go first.
	tables := []string{
		"run_log_entries",
		"activity_logs",
		"proxy_request_logs",
		"model_request_stats",
		"model_group_members",
		"model_groups",
		"default_model_settings",
		"runs",
		"comments",
		"attachments",
		"tasks",
		"skills",
		"agent_mcp_accounts",
		"agents",
		"llm_providers",
		"sprints",
		"projects",
		"companies",
		// MCP tables — clear dependents before mcp_servers (FK order)
		"agent_mcp_tool_filters",
		"mcp_tool_stats",
		"agent_mcp_servers",
		"mcp_accounts",
		"mcp_servers",
		// Identity graph last (everything above may reference users/teams).
		"team_invites",
		"password_reset_tokens",
		"sessions",
		"refresh_tokens",
		"web_authn_sessions",
		"web_authn_credentials",
		"team_members",
		"teams",
		"users",
	}
	for _, table := range tables {
		api.db.Exec("DELETE FROM " + table)
	}
	// Reset SQLite autoincrement so test IDs start at 1
	api.db.Exec("DELETE FROM sqlite_sequence")

	// Re-seed built-in MCP servers so tests that list servers get a consistent baseline.
	_ = db.New(api.db).EnsureBuiltinMCPServers(context.Background())

	// Recreate the e2e fixture user (auth is always on; the wipe removed it).
	// Its onUserCreated hook seeds the per-user builtin providers and default
	// model settings.
	if _, err := api.e2eUser(context.Background()); err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to recreate e2e user: "+err.Error())
		return
	}

	// Re-seed built-in LLM providers (OpenRouter, OpenCode Zen) so tests that
	// list providers get a consistent baseline. A wipe intentionally never
	// makes a live network call (stays fast and deterministic), so give them
	// an immediate placeholder catalog instead: without it, DefaultModel
	// would stay blank until the real, once-per-process background
	// discovery fetch completes — which, after the first wipe, may never
	// happen again — silently blocking anything that assumes a builtin
	// provider has a usable default (e.g. the "existing provider" onboarding
	// step's required Model Name field).
	q := db.New(api.db)
	seedPlaceholderModelCatalog(context.Background(), q)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// E2ERegister creates an account without a WebAuthn ceremony, for E2E tests
// that need a second real user (e.g. invite-join flows). It mirrors the passkey
// RegisterFinish account setup — create user, accept invite or make a team,
// seed builtins, unlock deterministically — and issues a session cookie.
func (api *API) E2ERegister(w http.ResponseWriter, r *http.Request) {
	if !utils.IsE2E() {
		http.Error(w, "not available", http.StatusForbidden)
		return
	}
	var req struct {
		Email       string `json:"email"`
		InviteToken string `json:"invite_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	email := db.NormalizeEmail(req.Email)
	if _, err := api.q.GetUserByEmail(r.Context(), email); err == nil {
		api.respondError(w, http.StatusConflict, "already exists")
		return
	}
	if req.InviteToken != "" {
		if _, err := api.q.GetTeamInviteByTokenHash(r.Context(), authctx.HashToken(req.InviteToken)); err != nil {
			api.respondError(w, http.StatusBadRequest, "invalid or expired invite")
			return
		}
	}
	user, err := api.q.CreateUser(r.Context(), email)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "create failed")
		return
	}
	// Email-bound invite acceptance (same rule as RegisterFinish); fall back to
	// a personal team on mismatch/no-invite.
	if !api.acceptTeamInviteFor(r.Context(), req.InviteToken, user) {
		_ = api.q.EnsureTeamForUser(r.Context(), user)
	}
	secrets.UnlockUser(user.ID, e2eDEK(), keyringTTL())
	api.seedNewUser(r.Context(), user.ID)
	// A distinct session cookie for this user (the fixture bypass only applies
	// to requests without a cookie).
	if err := api.issueTokenPair(w, r, user); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusCreated, map[string]any{"user": userResponse{ID: user.ID, Email: user.Email}})
}

// E2ELock evicts the fixture user's DEK so tests can exercise the locked
// vault state (crash re-tap). E2E-only.
func (api *API) E2ELock(w http.ResponseWriter, r *http.Request) {
	if !utils.IsE2E() {
		http.Error(w, "not available", http.StatusForbidden)
		return
	}
	if u, err := api.q.GetUserByEmail(r.Context(), e2eUserEmail); err == nil {
		secrets.LockUser(u.ID)
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "locked"})
}

// E2ERevealProviderSecret returns the DECRYPTED api_key of an LLM provider.
// E2E-only: the real API never exposes raw secrets (only has_api_key). This
// exists so the backup/restore e2e can prove a secret round-trips through the
// zero-knowledge pipeline — sealed as enc:u1 under the (deterministic, in E2E)
// user DEK, carried through backup + restore, then decrypted again after the
// fixture user is re-unlocked. A successful reveal is end-to-end proof the
// ciphertext survived and is still openable with the restored keyring.
func (api *API) E2ERevealProviderSecret(w http.ResponseWriter, r *http.Request) {
	if !utils.IsE2E() {
		http.Error(w, "not available", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "bad id")
		return
	}
	// Ensure the fixture user is unlocked (deterministic DEK) so the serializer
	// can decrypt on read.
	if _, err := api.e2eUser(r.Context()); err != nil {
		api.respondError(w, http.StatusInternalServerError, "unlock failed")
		return
	}
	p, err := api.q.GetLLMProvider(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "provider not found")
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"api_key": p.ApiKey})
}

// seedPlaceholderModelCatalog gives freshly re-seeded builtin providers a
// non-empty model catalog immediately, without any network call, so e2e
// tests that list/use providers right after a wipe never see a blank
// DefaultModel/SupportedModels. It's deliberately not a real model — a
// builtin provider's actual catalog only ever comes from a live
// pkg/llmdiscovery fetch (see main.go); production never calls this.
func seedPlaceholderModelCatalog(ctx context.Context, q *db.Queries) {
	providers, err := q.ListLLMProviders(ctx)
	if err != nil {
		return
	}
	for _, p := range providers {
		if !p.Builtin {
			continue
		}
		_ = q.UpdateLLMProviderModelCatalog(ctx, p.ID, []string{"e2e-placeholder-model"})
	}
}
