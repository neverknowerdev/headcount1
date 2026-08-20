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

func TestNormalizeAllowedMCPs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		err  bool
	}{
		{name: "empty means all", raw: "", want: ""},
		{name: "trims names", raw: `[" github ","linear"]`, want: `["github","linear"]`},
		{name: "empty list means none", raw: `[]`, want: `[]`},
		{name: "rejects invalid json", raw: `github`, err: true},
		{name: "rejects duplicates", raw: `["github","github"]`, err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeAllowedMCPs(tt.raw)
			if tt.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
