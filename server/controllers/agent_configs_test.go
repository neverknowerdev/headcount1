package endpoints

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListAgentConfigsReturnsTheBuiltinRoles(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/agent-configs", nil)

	(&API{}).ListAgentConfigs(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var configs []map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &configs))
	require.Len(t, configs, 13)

	var coder map[string]any
	for _, config := range configs {
		if config["name"] == "Coder" {
			coder = config
			break
		}
	}
	// A role is a name, a short name, a description and a one-line prompt;
	// nothing about models, tools or MCP access belongs to it any more.
	require.Equal(t, map[string]any{
		"name":        "Coder",
		"slug":        "CODER",
		"description": "Implements and debugs code from an approved technical specification.",
		"prompt":      "You are the Coder agent.",
	}, coder)
}
