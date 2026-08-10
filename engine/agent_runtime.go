package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/db"
)

func decodeAgentNames(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil
	}
	return names
}

// findAgentForRole resolves a delegation target from the company-scoped
// database Agent rows. RoleKey is the stable identity; Name remains useful for
// custom agents that have not been assigned a role key.
func (e *NativeEngine) findAgentForRole(ctx context.Context, companyID int32, role string) (db.Agent, error) {
	requested := strings.TrimSpace(role)
	if requested == "" {
		return db.Agent{}, fmt.Errorf("agent role/name is empty")
	}
	agents, err := e.q.ListAgentsByCompany(ctx, companyID)
	if err != nil {
		return db.Agent{}, fmt.Errorf("list agents for %q: %w", requested, err)
	}
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.RoleKey), requested) ||
			strings.EqualFold(strings.TrimSpace(agent.Name), requested) {
			return agent, nil
		}
	}
	return db.Agent{}, fmt.Errorf("no database agent matches %q", requested)
}
