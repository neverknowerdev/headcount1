package endpoints

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultCanUseWorkersForBuiltInRoles(t *testing.T) {
	require.True(t, defaultCanUseWorkers("CEO", "Custom CEO"))
	require.True(t, defaultCanUseWorkers("", "CTO"))
	require.True(t, defaultCanUseWorkers("CMO", ""))
	require.False(t, defaultCanUseWorkers("Researcher", "Researcher"))
	require.True(t, defaultCanUseWorkers("", "CEO Agent"))
	require.True(t, defaultCanUseWorkers("chief technology officer", "Technical Lead"))
}
