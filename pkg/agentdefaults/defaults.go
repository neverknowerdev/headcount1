// Package agentdefaults converts the checked-in agentconfig catalog into the
// database shape used by company bootstrap and upgrade seeding.
package agentdefaults

import (
	"encoding/json"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/aicli"
)

// Rows returns the initial database rows for every built-in role. Provider
// selection is intentionally left to the caller because it is tenant-owned.
func Rows(companyID int32) []db.Agent {
	configs := agentconfig.BuiltinConfigs()
	rows := make([]db.Agent, 0, len(configs))
	for _, cfg := range configs {
		allowedMCPs, _ := json.Marshal(cfg.AllowedMCPs)
		rows = append(rows, db.Agent{
			CompanyID:      companyID,
			Name:           cfg.Name,
			Builtin:        true,
			Enabled:        true,
			RoleKey:        cfg.Name,
			ShortName:      cfg.EffectiveShortName(),
			Description:    cfg.Description,
			SystemPrompt:   cfg.Prompt,
			ChatType:       string(cfg.ChatType),
			ReasoningLevel: string(cfg.ReasoningLevel),
			CanUseWorkers:  cfg.CanUseWorkers,
			AllowedMCPs:    string(allowedMCPs),
			Permissions:    PermissionsForConfig(cfg),
		})
	}
	return rows
}

// PermissionsForConfig returns the JSON-encoded deny map used to persist a
// built-in config's tool policy on an agent row. Templates use the same value
// so a newly created custom agent can inherit a built-in tool policy exactly.
func PermissionsForConfig(cfg *agentconfig.AgentConfig) string {
	permissions, _ := json.Marshal(initialPermissions(cfg))
	return string(permissions)
}

// initialPermissions stores only denials. This gives each built-in role a
// concrete, DB-owned tool policy while retaining the runtime's lifecycle tools
// and any future tools unless explicitly denied.
func initialPermissions(cfg *agentconfig.AgentConfig) map[string]string {
	permissions := make(map[string]string)
	toolNames := append([]string(nil), aicli.ConfigurableToolNames()...)
	toolNames = append(toolNames,
		string(aicli.ToolCallMCP),
		string(aicli.ToolDiscoverMCP),
		string(aicli.ToolCodegraphWildcard),
	)
	for _, name := range toolNames {
		if !cfg.IsToolAllowed(name) {
			permissions[name] = "deny"
		}
	}
	return permissions
}
