// Package agentdefaults converts the checked-in agentconfig catalog into the
// database shape used by company bootstrap and upgrade seeding.
package agentdefaults

import (
	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
)

// Rows returns the initial database rows for every built-in role.
func Rows(companyID int32) []db.Agent {
	configs := agentconfig.BuiltinConfigs()
	rows := make([]db.Agent, 0, len(configs))
	for _, cfg := range configs {
		rows = append(rows, db.Agent{
			CompanyID:    companyID,
			Name:         cfg.Name,
			Builtin:      true,
			Enabled:      true,
			RoleKey:      cfg.Name,
			ShortName:    cfg.ShortName,
			Description:  cfg.Description,
			SystemPrompt: cfg.Prompt,
		})
	}
	return rows
}
