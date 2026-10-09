package agentconfig_test

import (
	"testing"

	"agent-orchestrator/engine/agentconfig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The clean-slate migration resets built-in prompts with the same sentence,
// so the catalog and an upgraded database must agree on it.
func TestBuiltinConfigsAreRolesOnly(t *testing.T) {
	configs := agentconfig.BuiltinConfigs()
	require.Len(t, configs, 13)

	names := map[string]bool{}
	shortNames := map[string]bool{}
	for _, cfg := range configs {
		assert.NotEmpty(t, cfg.Description, cfg.Name)
		assert.Equal(t, "You are the "+cfg.Name+" agent.", cfg.Prompt)
		assert.LessOrEqual(t, len(cfg.ShortName), 7, cfg.Name)
		assert.NotEmpty(t, cfg.ShortName, cfg.Name)
		assert.False(t, names[cfg.Name], "duplicate name %s", cfg.Name)
		assert.False(t, shortNames[cfg.ShortName], "duplicate short name %s", cfg.ShortName)
		names[cfg.Name] = true
		shortNames[cfg.ShortName] = true
	}
	for _, role := range []string{"CEO", "CTO", "Coder", "QA Lead", "QA"} {
		assert.True(t, names[role], "the workflow needs the %s role", role)
	}
}
