package endpoints

import (
	"net/http"

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
}

// ListBuiltinAgents returns every compiled-in agent configuration. Unlike
// ListAgents, this is not scoped to a company — built-in configs are
// system-wide and identical across companies.
func (api *API) ListBuiltinAgents(w http.ResponseWriter, r *http.Request) {
	factory := agentconfig.NewDefaultFactory()
	names := factory.ListNames()

	out := make([]BuiltinAgentResponse, 0, len(names))
	for _, name := range names {
		cfg, err := factory.GetConfig(name)
		if err != nil {
			continue
		}
		out = append(out, BuiltinAgentResponse{
			Name:           cfg.Name,
			Description:    cfg.Description,
			Prompt:         cfg.Prompt,
			ChatType:       string(cfg.ChatType),
			ReasoningLevel: string(cfg.ReasoningLevel),
			AllowedTools:   cfg.AllowedTools,
			AllowedMCPs:    cfg.AllowedMCPs,
			Subagents:      cfg.Subagents,
			ParentAgent:    cfg.ParentAgent,
		})
	}

	api.respondJSON(w, http.StatusOK, out)
}
