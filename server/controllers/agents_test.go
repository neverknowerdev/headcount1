package endpoints

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultSubagentsForRole(t *testing.T) {
	require.Equal(t, `["CTO","CMO","Designer"]`, defaultSubagentsForRole("CEO", "Custom CEO"))
	require.Equal(t, `["Coder","Debugger","QA"]`, defaultSubagentsForRole("", "CTO"))
	require.Empty(t, defaultSubagentsForRole("Researcher", "Researcher"))
}
