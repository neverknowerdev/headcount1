package endpoints

import (
	"context"
	"encoding/json"
	"net/http"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/llmdiscovery"
	"agent-orchestrator/pkg/utils"
)

// WipeDB clears all data from the database. Only available when E2E_MODE=true.
// Route registration is guarded by the env var in main.go.
func (api *API) WipeDB(w http.ResponseWriter, r *http.Request) {
	if !utils.IsE2E() {
		http.Error(w, "WipeDB is only available in E2E mode", http.StatusForbidden)
		return
	}

	tables := []string{
		"activity_logs",
		"proxy_request_logs",
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
	}
	for _, table := range tables {
		api.db.Exec("DELETE FROM " + table)
	}
	// Reset SQLite autoincrement so test IDs start at 1
	api.db.Exec("DELETE FROM sqlite_sequence")

	// Re-seed built-in MCP servers so tests that list servers get a consistent baseline.
	_ = db.New(api.db).EnsureBuiltinMCPServers(context.Background())

	// Re-seed built-in LLM providers (OpenRouter, OpenCode Zen) so tests that
	// list providers get a consistent baseline. Their model catalog is
	// re-seeded from the curated no-network fallback list immediately
	// (never live-fetched here — a wipe should stay fast and offline), so a
	// wipe never leaves a provider with a blank DefaultModel: the real
	// startup-time discovery only ever runs once, before the first wipe.
	q := db.New(api.db)
	_ = q.EnsureBuiltinLLMProviders(context.Background())
	_ = llmdiscovery.SeedFallbackModels(context.Background(), q)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
