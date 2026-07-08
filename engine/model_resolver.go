package engine

import (
	"context"
	"fmt"
	"os"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
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

// resolveModel picks the best model to use for a run given:
//   - agentModel: the DB Agent's own model override (may be "")
//   - provider:   the configured LLM provider
//   - cfg:        the optional agent config (may be nil)
//
// Selection rules (first match wins):
//  1. If cfg has AllowedModels, use the first one that appears in the
//     provider's SupportedModels list.
//  2. If cfg has AllowedModels but none match, return an error so the
//     misconfiguration is surfaced immediately rather than silently
//     using an unsupported model.
//  3. If cfg has no AllowedModels (or cfg is nil), use agentModel if
//     set, otherwise the provider's DefaultModel.
//  4. If no model can be determined at all, return an error.
func resolveModel(cfg *agentconfig.AgentConfig, provider db.LLMProvider, agentModel string) (string, error) {
	if cfg != nil && len(cfg.AllowedModels) > 0 {
		supported := parseSupportedModels(provider.SupportedModels)
		supportedSet := make(map[string]bool, len(supported))
		for _, m := range supported {
			supportedSet[m] = true
		}

		// If the provider has no SupportedModels at all, fall back to
		// DefaultModel without strict validation (provider may not have
		// populated the list yet).
		if len(supported) == 0 {
			if provider.DefaultModel != "" {
				return provider.DefaultModel, nil
			}
			return "", fmt.Errorf("provider %q has no default model configured", provider.Name)
		}

		for _, preferred := range cfg.AllowedModels {
			if supportedSet[preferred] {
				return preferred, nil
			}
		}
		return "", fmt.Errorf(
			"agent config %q requires one of %v but provider %q only supports %v",
			cfg.Name, cfg.AllowedModels, provider.Name, supported,
		)
	}

	// No AllowedModels constraint — use agent override or provider default.
	if agentModel != "" {
		return agentModel, nil
	}
	if provider.DefaultModel != "" {
		return provider.DefaultModel, nil
	}
	return "", fmt.Errorf("provider %q has no default model configured", provider.Name)
}

// resolveProvider resolves the LLM provider+model target for a run. Agents
// bound to a model group (agent.ModelGroupID set) get a synthetic provider
// pointing at the local group-router gateway — free-first ordering,
// failover, and stats collection all happen there, keyed by the group's
// slug used as the "model" name. Otherwise the agent's fixed provider is
// loaded directly and its model resolved via resolveModel.
func resolveProvider(ctx context.Context, q *db.Queries, agent db.Agent, agentCfg *agentconfig.AgentConfig) (db.LLMProvider, string, error) {
	if agent.ModelGroupID != nil {
		group, err := q.GetModelGroup(ctx, *agent.ModelGroupID)
		if err != nil {
			return db.LLMProvider{}, "", fmt.Errorf("failed to get model group: %w", err)
		}
		if len(group.Members) == 0 {
			return db.LLMProvider{}, "", fmt.Errorf("model group %q has no members", group.Name)
		}
		provider := db.LLMProvider{
			Name:         group.Name + " (model group)",
			BaseUrl:      modelGroupProxyBaseURL(group.Slug),
			ProviderType: "openai",
		}
		return provider, group.Slug, nil
	}

	if agent.ProviderID == nil {
		return db.LLMProvider{}, "", fmt.Errorf("agent has no provider or model group configured")
	}
	provider, err := q.GetLLMProvider(ctx, *agent.ProviderID)
	if err != nil {
		return db.LLMProvider{}, "", fmt.Errorf("failed to get provider: %w", err)
	}
	model, err := resolveModel(agentCfg, provider, agent.Model)
	if err != nil {
		return db.LLMProvider{}, "", fmt.Errorf("model resolution failed: %w", err)
	}
	return provider, model, nil
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
