package endpoints

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListAgentConfigsIncludesPromptAndPersistedToolPermissions(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/agent-configs", nil)

	(&API{}).ListAgentConfigs(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var configs []AgentConfigResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &configs))
	require.Len(t, configs, 13)

	var coder AgentConfigResponse
	for _, config := range configs {
		if config.Name == "Coder" {
			coder = config
			break
		}
	}
	require.Equal(t, "Coder", coder.Name)
	require.Equal(t, "Coder", coder.CanonicalName)
	require.Equal(t, "CODER", coder.Slug)
	require.NotEmpty(t, coder.Prompt)
	require.NotEmpty(t, coder.BestModels)
	require.Contains(t, coder.AllowedTools, "read")
	require.Contains(t, coder.AllowedTools, "write")

	var permissions map[string]string
	require.NoError(t, json.Unmarshal([]byte(coder.Permissions), &permissions))
	require.NotContains(t, permissions, "read")
	require.NotContains(t, permissions, "write")
	require.Equal(t, "deny", permissions["browser_use"])
	require.Equal(t, "deny", permissions["call_mcp_tool"])
}
