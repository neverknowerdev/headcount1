package endpoints

import (
	"net/http"

	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/pkg/agentdefaults"
)

// AgentConfigResponse is the wire shape for a built-in role template. These
// values are read-only bootstrap/documentation data; runtime settings live on
// the company's db.Agent row.
type AgentConfigResponse struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Prompt         string   `json:"prompt"`
	ChatType       string   `json:"chat_type"`
	ReasoningLevel string   `json:"reasoning_level"`
	Subagents      []string `json:"subagents"`
	ParentAgent    string   `json:"parent_agent,omitempty"`
	AllowedTools   []string `json:"allowed_tools,omitempty"`
	Permissions    string   `json:"permissions"`
}

// ListAgentConfigs returns built-in role templates (CEO, CTO, CMO, Coder, …)
// in canonical order so the Agents page can display available defaults
// alongside the company's database-owned agents.
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
			Permissions:    agentdefaults.PermissionsForConfig(cfg),
		})
	}
	api.respondJSON(w, http.StatusOK, out)
}
