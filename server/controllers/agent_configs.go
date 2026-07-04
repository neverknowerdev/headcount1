package endpoints

import (
	"net/http"

	"agent-orchestrator/engine/agentconfig"
)

// AgentConfigResponse is the wire shape for a built-in agent configuration.
// The prompt itself is included so the UI can show what each built-in agent
// is instructed to do.
type AgentConfigResponse struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Prompt         string   `json:"prompt"`
	ChatType       string   `json:"chat_type"`
	ReasoningLevel string   `json:"reasoning_level"`
	Subagents      []string `json:"subagents"`
	ParentAgent    string   `json:"parent_agent,omitempty"`
	AllowedTools   []string `json:"allowed_tools,omitempty"`
}

// ListAgentConfigs returns the built-in agent configurations (CEO, CTO,
// CMO, Coder, …) in their canonical order so the Agents page can
// display them alongside the company's own agents.
func (api *API) ListAgentConfigs(w http.ResponseWriter, r *http.Request) {
	configs := agentconfig.BuiltinConfigs()
	out := make([]AgentConfigResponse, 0, len(configs))
	for _, cfg := range configs {
		out = append(out, AgentConfigResponse{
			Name:           cfg.Name,
			Description:    cfg.Description,
			Prompt:         cfg.Prompt,
			ChatType:       string(cfg.ChatType),
			ReasoningLevel: string(cfg.ReasoningLevel),
			Subagents:      cfg.Subagents,
			ParentAgent:    cfg.ParentAgent,
			AllowedTools:   cfg.AllowedTools,
		})
	}
	api.respondJSON(w, http.StatusOK, out)
}
