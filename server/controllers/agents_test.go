package endpoints

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultSubagentsForRole(t *testing.T) {
	require.Equal(t, `["CTO","CMO","UX Designer","Graphic Designer"]`, defaultSubagentsForRole("CEO", "Custom CEO"))
	require.Equal(t, `["Coder","QA Lead","QA Manual","QA","Debugger"]`, defaultSubagentsForRole("", "CTO"))
	require.Empty(t, defaultSubagentsForRole("Researcher", "Researcher"))
}
