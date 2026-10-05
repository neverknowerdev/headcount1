package agentconfig_test

import (
	"testing"

	"agent-orchestrator/engine/agentconfig"
	"github.com/stretchr/testify/require"
)

func TestRoleMatchesNormalizesBuiltInDisplayLabels(t *testing.T) {
	require.True(t, agentconfig.RoleMatches("", "CEO Agent", "CEO"))
	require.True(t, agentconfig.RoleMatches("chief-technology-officer", "custom name", "CTO"))
	require.True(t, agentconfig.RoleMatches("", "Quality Assurance Agent", "QA"))
	require.False(t, agentconfig.RoleMatches("researcher", "CEO Agent", "CTO"))
}
