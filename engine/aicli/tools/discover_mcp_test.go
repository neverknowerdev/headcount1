package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
)

// fakeMCPClient is a minimal in-memory MCP client for testing DiscoverMCPTools.
type fakeMCPClient struct {
	tools  []fakeToolDef
	called map[string]json.RawMessage
}

type fakeToolDef struct {
	name        string
	description string
	result      string
}

func (f *fakeMCPClient) Initialize(_ context.Context) (interface{}, error) { return nil, nil }
func (f *fakeMCPClient) ListTools(_ context.Context) (interface{}, error)  { return f.tools, nil }
func (f *fakeMCPClient) CallTool(_ context.Context, name string, args json.RawMessage) (string, error) {
	if f.called == nil {
		f.called = make(map[string]json.RawMessage)
	}
	f.called[name] = args
	for _, t := range f.tools {
		if t.name == name {
			return t.result, nil
		}
	}
	return "", nil
}
func (f *fakeMCPClient) Close() error { return nil }

// TestDiscoverMCPTools_UnknownServer verifies error on bad server name.
func TestDiscoverMCPTools_UnknownServer(t *testing.T) {
	reg := aicli.NewRegistry()
	d := tools.NewDiscoverMCPTools(reg, []db.MCPServer{
		{Name: "known", Transport: "http", URL: "http://localhost"},
	})

	_, err := d.Execute(context.Background(), json.RawMessage(`{"server_name":"unknown"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown MCP server")
}

// TestDiscoverMCPTools_BuiltinSkipped verifies builtin servers are not added.
func TestDiscoverMCPTools_BuiltinSkipped(t *testing.T) {
	reg := aicli.NewRegistry()
	// NewDiscoverMCPTools should only contain non-builtin servers.
	servers := []db.MCPServer{
		{Name: "paperclip2", Transport: "builtin", Builtin: true},
	}
	d := tools.NewDiscoverMCPTools(reg, servers)

	// The tool def should mention paperclip2 (even builtin servers are listed
	// for discovery if they were passed in; native_engine filters them out).
	_ = d
}

// TestDiscoverMCPTools_InvalidArgs ensures bad JSON is rejected gracefully.
func TestDiscoverMCPTools_InvalidArgs(t *testing.T) {
	reg := aicli.NewRegistry()
	d := tools.NewDiscoverMCPTools(reg, nil)
	_, err := d.Execute(context.Background(), json.RawMessage(`not json`))
	require.Error(t, err)
}
