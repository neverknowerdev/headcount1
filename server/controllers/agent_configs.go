package endpoints

import (
	"net/http"

	"agent-orchestrator/engine/agentconfig"
)

// AgentConfigResponse is the wire shape of a built-in role. It is read-only
// reference data; what an agent actually says lives on the company's row.
type AgentConfigResponse struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// ListAgentConfigs returns the built-in roles in canonical order.
func (api *API) ListAgentConfigs(w http.ResponseWriter, r *http.Request) {
	configs := agentconfig.BuiltinConfigs()
	out := make([]AgentConfigResponse, 0, len(configs))
	for _, cfg := range configs {
		out = append(out, AgentConfigResponse{
			Name:        cfg.Name,
			Slug:        cfg.ShortName,
			Description: cfg.Description,
			Prompt:      cfg.Prompt,
		})
	}
	api.respondJSON(w, http.StatusOK, out)
}
