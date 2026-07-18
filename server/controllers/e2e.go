package endpoints

import (
	"context"
	"encoding/json"
	"net/http"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/utils"
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
		"user_keys",
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
