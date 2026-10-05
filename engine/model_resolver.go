package engine

import (
	"context"
	"fmt"
	"os"
	"strings"

	"agent-orchestrator/db"
)

// parseSupportedModels splits the provider's comma-separated SupportedModels
// string into a trimmed, non-empty slice.
func parseSupportedModels(s string) []string {
	var out []string
	for _, m := range strings.Split(s, ",") {
		if t := strings.TrimSpace(m); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// resolveModel picks the model configured on the database Agent. The provider
// default is only a fallback for legacy rows that have no explicit model.
func resolveModel(provider db.LLMProvider, agentModel string) (string, error) {
	if agentModel != "" {
		return agentModel, nil
	}
	if provider.DefaultModel != "" {
		return provider.DefaultModel, nil
	}
	return "", fmt.Errorf("provider %q has no default model configured", provider.Name)
}

// resolveProvider resolves the LLM provider+model target for a run from the
// database Agent only. Agents bound to a model group (agent.ModelGroupID set)
// get a synthetic provider pointing at the local group-router gateway.
func resolveProvider(ctx context.Context, q *db.Queries, agent db.Agent) (db.LLMProvider, string, error) {
	if agent.ModelGroupID != nil {
		group, err := q.GetModelGroup(ctx, *agent.ModelGroupID)
		if err != nil {
			return db.LLMProvider{}, "", fmt.Errorf("failed to get model group: %w", err)
		}
		return resolveModelGroupTarget(group)
	}

	if agent.ProviderID == nil {
		return db.LLMProvider{}, "", fmt.Errorf("agent has no provider or model group configured")
	}
	provider, err := q.GetLLMProvider(ctx, *agent.ProviderID)
	if err != nil {
		return db.LLMProvider{}, "", fmt.Errorf("failed to get provider: %w", err)
	}
	model, err := resolveModel(provider, agent.Model)
	if err != nil {
		return db.LLMProvider{}, "", fmt.Errorf("model resolution failed: %w", err)
	}
	return provider, model, nil
}

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
// failover, and stats collection for engine-driven runs.
func modelGroupProxyBaseURL(slug string) string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return fmt.Sprintf("http://127.0.0.1:%s/api/proxy/group/%s", port, slug)
}
