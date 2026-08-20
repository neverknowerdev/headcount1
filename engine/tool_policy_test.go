package engine

import (
	"testing"

	"agent-orchestrator/db"
	"github.com/stretchr/testify/require"
)

func toolSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

func TestRoleToolNamesEnforceBuiltInBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		agent     db.Agent
		allowed   []string
		forbidden []string
	}{
		{
			name:      "CEO delegates without implementation access",
			agent:     db.Agent{Name: "CEO Agent", RoleKey: "CEO"},
			allowed:   []string{"ask_human", "create_task", "create_subtask", "finish_task"},
			forbidden: []string{"read", "write", "bash", "browser_use", "run_worker"},
		},
		{
			name:      "CTO researches without mutating the workspace",
			agent:     db.Agent{Name: "Chief Technology Officer", RoleKey: "chief-technology-officer"},
			allowed:   []string{"read", "grep", "codegraph_*", "write_artifact", "run_worker"},
			forbidden: []string{"write", "bash", "browser_use", "ask_human"},
		},
		{
			name:      "Coder implements",
			agent:     db.Agent{Name: "Coder", RoleKey: "backend"},
			allowed:   []string{"read", "write", "bash", "ask_task_owner", "finish_task"},
			forbidden: []string{"ask_human", "run_worker", "browser_use"},
		},
		{
			name:      "QA verifies without editing",
			agent:     db.Agent{Name: "QA", RoleKey: "qa"},
			allowed:   []string{"read", "bash", "browser_use", "ask_task_owner", "finish_task"},
			forbidden: []string{"write", "ask_human", "run_worker"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := toolSet(roleToolNames(tt.agent))
			for _, name := range tt.allowed {
				if name == "codegraph_*" {
					require.True(t, set["codegraph_*"], "role should include codegraph wildcard")
					continue
				}
				require.True(t, set[name], "role should allow %s", name)
			}
			for _, name := range tt.forbidden {
				require.False(t, set[name], "role should forbid %s", name)
			}
		})
	}
}

func TestRoleToolNamesPreserveCustomRoleCompatibility(t *testing.T) {
	require.Empty(t, roleToolNames(db.Agent{Name: "Researcher", RoleKey: "researcher"}))
}
