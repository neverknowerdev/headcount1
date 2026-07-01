package endpoints

import (
	"net/http"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
)

// BuiltinAgentResponse describes a compiled-in agent configuration (the
// SmartPlanner, researchers, Coder, Tester, etc. used by the AskMode
// pipeline, plus the legacy CEO/CTO/Programmer/QA/Writer/Researcher roles).
// These are not rows in the agents table — they're system-wide, read-only
// configs defined in engine/agentconfig.
type BuiltinAgentResponse struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Prompt         string   `json:"prompt"`
	ChatType       string   `json:"chat_type"`
	ReasoningLevel string   `json:"reasoning_level"`
	AllowedTools   []string `json:"allowed_tools"`
	AllowedMCPs    []string `json:"allowed_mcps"`
	Subagents      []string `json:"subagents"`
	ParentAgent    string   `json:"parent_agent"`
	// ResolvedProvider and ResolvedModel are only populated for the AskMode
	// pipeline roles (SmartPlanner, the researchers, Coder, Tester), which
	// are configurable via Role Model Configuration on the LLM Providers page.
	// Empty for legacy roles (CEO, CTO, ...), whose model comes from whichever
	// company Agent they're assigned to at runtime.
	ResolvedProvider string `json:"resolved_provider"`
	ResolvedModel    string `json:"resolved_model"`
}

// ListBuiltinAgents returns every compiled-in agent configuration. Unlike
// ListAgents, this is not scoped to a company — built-in configs are
// system-wide and identical across companies.
func (api *API) ListBuiltinAgents(w http.ResponseWriter, r *http.Request) {
	factory := agentconfig.NewDefaultFactory()
	names := factory.ListNames()

	settings := LoadSettings()
	providers, _ := api.q.ListLLMProviders(r.Context())

	out := make([]BuiltinAgentResponse, 0, len(names))
	for _, name := range names {
		cfg, err := factory.GetConfig(name)
		if err != nil {
			continue
		}
		resp := BuiltinAgentResponse{
			Name:           cfg.Name,
			Description:    cfg.Description,
			Prompt:         cfg.Prompt,
			ChatType:       string(cfg.ChatType),
			ReasoningLevel: string(cfg.ReasoningLevel),
			AllowedTools:   cfg.AllowedTools,
			AllowedMCPs:    cfg.AllowedMCPs,
			Subagents:      cfg.Subagents,
			ParentAgent:    cfg.ParentAgent,
		}
		if providerID, model, ok := roleModelFor(name, settings.RoleModels); ok {
			if providerID == 0 {
				providerID = settings.DefaultProviderID
			}
			resp.ResolvedProvider, resp.ResolvedModel = resolveProviderAndModel(providerID, model, providers)
		}
		out = append(out, resp)
	}

	api.respondJSON(w, http.StatusOK, out)
}

// roleModelFor returns the configured provider ID and model override for the
// given built-in agent name, and whether that name is an AskMode pipeline
// role (only those are configurable via Role Model Configuration).
func roleModelFor(name string, rm RoleModelConfig) (providerID int32, model string, ok bool) {
	switch name {
	case "SmartPlanner":
		return rm.SmartPlannerProviderID, rm.SmartPlannerModel, true
	case "TechSpecResearcher":
		return rm.TechResearcherProviderID, rm.TechResearcherModel, true
	case "WritingSpecResearcher":
		return rm.WritingResearcherProviderID, rm.WritingResearcherModel, true
	case "DesignSpecResearcher":
		return rm.DesignResearcherProviderID, rm.DesignResearcherModel, true
	case "Coder":
		return rm.CoderProviderID, rm.CoderModel, true
	case "Tester":
		return rm.TesterProviderID, rm.TesterModel, true
	default:
		return 0, "", false
	}
}

// resolveProviderAndModel mirrors the orchestrator's fallback logic: an
// explicit provider ID wins; otherwise fall back to the first available
// provider. The model override wins if set; otherwise the provider's default.
func resolveProviderAndModel(providerID int32, model string, providers []db.LLMProvider) (providerName, resolvedModel string) {
	var provider *db.LLMProvider
	if providerID != 0 {
		for i := range providers {
			if providers[i].ID == providerID {
				provider = &providers[i]
				break
			}
		}
	} else if len(providers) > 0 {
		provider = &providers[0]
	}
	if provider == nil {
		return "", model
	}
	resolvedModel = model
	if resolvedModel == "" {
		resolvedModel = provider.DefaultModel
	}
	return provider.Name, resolvedModel
}
