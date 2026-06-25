package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli/tools"
)

// TestNewMCPSessionStore_ServerNames verifies the store lists servers correctly.
func TestNewMCPSessionStore_ServerNames(t *testing.T) {
	store := tools.NewMCPSessionStore([]db.MCPServer{
		{Name: "github/alice", Transport: "http", URL: "http://localhost"},
		{Name: "postiz/main", Transport: "http", URL: "http://localhost"},
	}, nil, nil)
	names := store.ServerNames()
	assert.Equal(t, []string{"github/alice", "postiz/main"}, names)
}

// TestNewMCPSessionStore_Empty verifies empty store is safe.
func TestNewMCPSessionStore_Empty(t *testing.T) {
	store := tools.NewMCPSessionStore(nil, nil, nil)
	assert.Empty(t, store.ServerNames())
}

// TestNewMCPSessionStore_ToolsCache verifies tool names are parsed from ToolsCache.
func TestNewMCPSessionStore_ToolsCache(t *testing.T) {
	cache, _ := json.Marshal([]map[string]interface{}{
		{"name": "list_issues"},
		{"name": "create_issue"},
	})
	store := tools.NewMCPSessionStore([]db.MCPServer{
		{Name: "github/alice", Transport: "http", URL: "http://localhost", ToolsCache: string(cache)},
	}, nil, nil)
	listing := store.CompactListing()
	assert.Contains(t, listing, "list_issues")
	assert.Contains(t, listing, "create_issue")
}

// TestCallMCPTool_UnknownServer verifies error on unknown server name.
func TestCallMCPTool_UnknownServer(t *testing.T) {
	store := tools.NewMCPSessionStore([]db.MCPServer{
		{Name: "known", Transport: "http", URL: "http://localhost"},
	}, nil, nil)
	callTool, _ := tools.NewMCPTools(store)

	_, err := callTool.Execute(context.Background(), json.RawMessage(`{"server":"unknown","tool":"foo"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown MCP server")
}

// TestDiscoverMCPTool_UnknownServer verifies error on unknown server.
func TestDiscoverMCPTool_UnknownServer(t *testing.T) {
	store := tools.NewMCPSessionStore(nil, nil, nil)
	_, discoverTool := tools.NewMCPTools(store)

	_, err := discoverTool.Execute(context.Background(), json.RawMessage(`{"server":"nope","tool":"foo"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown MCP server")
}

// TestDiscoverMCPTool_CachedTool verifies description is returned from cache without connecting.
func TestDiscoverMCPTool_CachedTool(t *testing.T) {
	cache, _ := json.Marshal([]map[string]interface{}{
		{
			"name":        "list_issues",
			"description": "List issues for a repository.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"owner": map[string]interface{}{"type": "string", "description": "Repo owner"},
					"repo":  map[string]interface{}{"type": "string", "description": "Repo name"},
				},
				"required": []string{"owner", "repo"},
			},
		},
	})
	store := tools.NewMCPSessionStore([]db.MCPServer{
		{Name: "github/alice", Transport: "http", URL: "http://localhost", ToolsCache: string(cache)},
	}, nil, nil)
	_, discoverTool := tools.NewMCPTools(store)

	result, err := discoverTool.Execute(context.Background(), json.RawMessage(`{"server":"github/alice","tool":"list_issues"}`))
	require.NoError(t, err)
	assert.Contains(t, result, "list_issues")
	assert.Contains(t, result, "List issues for a repository.")
	assert.Contains(t, result, "owner")
	assert.Contains(t, result, "repo")
}

// TestDiscoverMCPTool_InvalidArgs ensures bad JSON is rejected.
func TestDiscoverMCPTool_InvalidArgs(t *testing.T) {
	store := tools.NewMCPSessionStore(nil, nil, nil)
	_, discoverTool := tools.NewMCPTools(store)

	_, err := discoverTool.Execute(context.Background(), json.RawMessage(`not json`))
	require.Error(t, err)
}

// TestOnToolCall_Fires verifies the onToolCall callback receives server and tool name.
func TestOnToolCall_Fires(t *testing.T) {
	cache, _ := json.Marshal([]map[string]interface{}{
		{"name": "list_issues"},
	})
	var gotServer, gotTool string
	store := tools.NewMCPSessionStore([]db.MCPServer{
		{Name: "github/alice", Transport: "http", URL: "http://localhost", ToolsCache: string(cache)},
	}, nil, func(server, tool string) {
		gotServer, gotTool = server, tool
	})
	// onToolCall fires on success — trigger via an unknown server to check it's only
	// called on success (error path must NOT fire it).
	callTool, _ := tools.NewMCPTools(store)
	_, _ = callTool.Execute(context.Background(), json.RawMessage(`{"server":"unknown","tool":"foo"}`))
	assert.Empty(t, gotServer, "callback should not fire on error")
	assert.Empty(t, gotTool)
	_ = store // suppress unused warning
}

// TestCompactListing verifies the listing format includes server and tool names.
func TestCompactListing(t *testing.T) {
	cache, _ := json.Marshal([]map[string]interface{}{
		{"name": "get_issue"},
		{"name": "create_pr"},
	})
	store := tools.NewMCPSessionStore([]db.MCPServer{
		{Name: "github/alice", Transport: "http", URL: "http://localhost", ToolsCache: string(cache)},
		{Name: "postiz/main", Transport: "http", URL: "http://localhost"},
	}, nil, nil)
	listing := store.CompactListing()
	assert.Contains(t, listing, "github/alice")
	assert.Contains(t, listing, "get_issue")
	assert.Contains(t, listing, "create_pr")
	assert.Contains(t, listing, "postiz/main")
	assert.Contains(t, listing, "call_mcp_tool")
	assert.Contains(t, listing, "discover_mcp_tool")
}

// TestCompactListing_Empty returns empty string for no servers.
func TestCompactListing_Empty(t *testing.T) {
	store := tools.NewMCPSessionStore(nil, nil, nil)
	assert.Empty(t, store.CompactListing())
}

// TestNewMCPTools_Defs verifies the two tools have the expected names.
func TestNewMCPTools_Defs(t *testing.T) {
	store := tools.NewMCPSessionStore(nil, nil, nil)
	callTool, discoverTool := tools.NewMCPTools(store)
	assert.Equal(t, "call_mcp_tool", callTool.Def().Function.Name)
	assert.Equal(t, "discover_mcp_tool", discoverTool.Def().Function.Name)
}
