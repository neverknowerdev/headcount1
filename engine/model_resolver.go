package engine

import (
	"fmt"
	"os"

	"agent-orchestrator/db"
)

// resolveModelGroupTarget returns the synthetic provider used to address the
// in-process model-group gateway. The concrete provider and model are chosen
// by the gateway for every request; selecting a member here would bypass
// free-first ordering, cooldowns, failover, and request statistics.
func resolveModelGroupTarget(group db.ModelGroup) (db.LLMProvider, string, error) {
	if len(db.ExpandModelGroupMembers(group.Members)) == 0 {
		return db.LLMProvider{}, "", fmt.Errorf("model group %q has no members", group.Name)
	}
	provider := db.LLMProvider{
		Name:         group.Name + " (model group)",
		BaseUrl:      modelGroupProxyBaseURL(group.Slug),
		ProviderType: "openai",
	}
	return provider, group.Slug, nil
}

// modelGroupProxyBaseURL returns the local gateway base URL for a model
// group. The engine runs in the same process as the HTTP server, so this
// localhost round-trip reuses the group router's free-first ordering,
// failover, and stats collection for engine-driven calls.
func modelGroupProxyBaseURL(slug string) string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return fmt.Sprintf("http://127.0.0.1:%s/api/proxy/group/%s", port, slug)
}
