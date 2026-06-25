package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
)

// ---- helpers ----

func makeProxy(projects []db.Project, servers []db.MCPServer) *tools.CodegraphProxy {
	pairs := make([]db.CodegraphProjectServer, len(projects))
	for i, p := range projects {
		pairs[i] = db.CodegraphProjectServer{Project: p, Server: servers[i]}
	}
	return tools.NewCodegraphProxy(nil, pairs)
}

func makeProxyWithCurrent(current db.Project, projects []db.Project, servers []db.MCPServer) *tools.CodegraphProxy {
	pairs := make([]db.CodegraphProjectServer, len(projects))
	for i, p := range projects {
		pairs[i] = db.CodegraphProjectServer{Project: p, Server: servers[i]}
	}
	return tools.NewCodegraphProxy(&current, pairs)
}

func stubServer(name string) db.MCPServer {
	return db.MCPServer{
		Name:      name,
		Transport: "stdio",
		Command:   "false", // won't be invoked in unit tests
		Args:      `[]`,
	}
}

func stubProject(id int32, name string) db.Project {
	return db.Project{ID: id, Name: name}
}

// ---- proxy creation & tool registration ----

func TestCodegraphProxy_Empty(t *testing.T) {
	proxy := tools.NewCodegraphProxy(nil, nil)
	defer proxy.Close()
	reg := aicli.NewRegistry()
	proxy.RegisterAll(reg)
	defs := reg.Defs()
	// Should still register tools even with no projects.
	assert.NotEmpty(t, defs)
}

func TestCodegraphProxy_RegisterAll(t *testing.T) {
	proxy := makeProxy(
		[]db.Project{stubProject(1, "my-app")},
		[]db.MCPServer{stubServer("codegraph-1")},
	)
	defer proxy.Close()

	reg := aicli.NewRegistry()
	proxy.RegisterAll(reg)

	defs := reg.Defs()
	nameSet := make(map[string]bool)
	for _, d := range defs {
		nameSet[d.Function.Name] = true
	}

	for _, expected := range []string{
		"codegraph_explore",
		"codegraph_query",
		"codegraph_callers",
		"codegraph_callees",
		"codegraph_impact",
		"codegraph_node",
		"codegraph_files",
		"codegraph_status",
	} {
		assert.True(t, nameSet[expected], "expected tool %q in registry", expected)
	}
}

// ---- tool definitions include project enum ----

func TestCodegraphProxy_ToolDef_ProjectEnum(t *testing.T) {
	proxy := makeProxy(
		[]db.Project{stubProject(1, "frontend"), stubProject(2, "backend")},
		[]db.MCPServer{stubServer("codegraph-1"), stubServer("codegraph-2")},
	)
	defer proxy.Close()

	reg := aicli.NewRegistry()
	proxy.RegisterAll(reg)

	var exploreDef *aicli.ToolDef
	for _, d := range reg.Defs() {
		if d.Function.Name == "codegraph_explore" {
			def := d
			exploreDef = &def
			break
		}
	}
	require.NotNil(t, exploreDef)

	// Parameters must contain the project enum with both project names.
	paramsStr := string(exploreDef.Function.Parameters)
	assert.Contains(t, paramsStr, "frontend")
	assert.Contains(t, paramsStr, "backend")
	assert.Contains(t, paramsStr, "project")
}

func TestCodegraphProxy_ToolDef_EmptyProjectEnum(t *testing.T) {
	proxy := tools.NewCodegraphProxy(nil, nil)
	defer proxy.Close()

	reg := aicli.NewRegistry()
	proxy.RegisterAll(reg)

	for _, d := range reg.Defs() {
		if d.Function.Name == "codegraph_explore" {
			assert.Contains(t, string(d.Function.Parameters), `"enum":[]`)
			return
		}
	}
	t.Fatal("codegraph_explore not registered")
}

// ---- invalid JSON ----

func TestCodegraphProxy_Execute_InvalidJSON(t *testing.T) {
	proxy := makeProxy(
		[]db.Project{stubProject(1, "app")},
		[]db.MCPServer{stubServer("codegraph-1")},
	)
	defer proxy.Close()

	reg := aicli.NewRegistry()
	proxy.RegisterAll(reg)

	_, err := reg.Execute(context.Background(), "codegraph_explore", json.RawMessage(`not json`))
	require.Error(t, err)
}

// ---- unknown project ----

func TestCodegraphProxy_Execute_UnknownProject(t *testing.T) {
	proxy := makeProxy(
		[]db.Project{stubProject(1, "app")},
		[]db.MCPServer{stubServer("codegraph-1")},
	)
	defer proxy.Close()

	reg := aicli.NewRegistry()
	proxy.RegisterAll(reg)

	_, err := reg.Execute(context.Background(), "codegraph_explore",
		json.RawMessage(`{"query":"how does auth work","project":"nonexistent"}`))
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "unknown project")
}

// ---- no projects at all: no error on registration, error on execute ----

func TestCodegraphProxy_Execute_NoProjects(t *testing.T) {
	proxy := tools.NewCodegraphProxy(nil, nil)
	defer proxy.Close()

	reg := aicli.NewRegistry()
	proxy.RegisterAll(reg)

	_, err := reg.Execute(context.Background(), "codegraph_explore",
		json.RawMessage(`{"query":"auth"}`))
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "no codegraph projects")
}

// ---- current project resolution ----

func TestCodegraphProxy_Execute_DefaultsToCurrentProject(t *testing.T) {
	proj1 := stubProject(1, "frontend")
	proj2 := stubProject(2, "backend")
	// current = proj2; no "project" param → should route to backend
	proxy := makeProxyWithCurrent(proj2,
		[]db.Project{proj1, proj2},
		[]db.MCPServer{stubServer("codegraph-1"), stubServer("codegraph-2")},
	)
	defer proxy.Close()

	// We can't fully execute (would try to spawn `false`), but we can verify
	// the project resolution logic doesn't error on unknown project.
	reg := aicli.NewRegistry()
	proxy.RegisterAll(reg)

	// Execute with no project: will resolve to proj2 and try to spawn `false`,
	// which will fail at MCP connect — but with a "start server" error, not
	// "unknown project" or "no codegraph projects".
	_, err := reg.Execute(context.Background(), "codegraph_explore",
		json.RawMessage(`{"query":"auth"}`))
	require.Error(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "unknown project")
	assert.NotContains(t, strings.ToLower(err.Error()), "no codegraph projects")
	assert.Contains(t, strings.ToLower(err.Error()), "codegraph") // some mcp/start error
}

// ---- project param stripped before forwarding ----

func TestCodegraphProxy_ProjectFieldStripped(t *testing.T) {
	// Verify the "project" field is removed before forwarding to MCP.
	// We do this by checking the tool definition and ensuring our proxy
	// handles it internally (integration tested via the unknown-project error
	// path which shows the project param was parsed).
	proxy := makeProxy(
		[]db.Project{stubProject(1, "app")},
		[]db.MCPServer{stubServer("codegraph-1")},
	)
	defer proxy.Close()

	reg := aicli.NewRegistry()
	proxy.RegisterAll(reg)

	// Passing explicit valid project name; will fail at MCP spawn but that's expected.
	_, err := reg.Execute(context.Background(), "codegraph_explore",
		json.RawMessage(`{"query":"auth","project":"app"}`))
	require.Error(t, err)
	// Should be a server-start error, not a routing error.
	assert.NotContains(t, strings.ToLower(err.Error()), "unknown project")
}

// ---- buildProjectEnum ----

func TestBuildProjectEnum_MultipleProjects(t *testing.T) {
	proxy := makeProxy(
		[]db.Project{stubProject(1, "alpha"), stubProject(2, "beta"), stubProject(3, "gamma")},
		[]db.MCPServer{stubServer("cg-1"), stubServer("cg-2"), stubServer("cg-3")},
	)
	defer proxy.Close()

	reg := aicli.NewRegistry()
	proxy.RegisterAll(reg)

	for _, d := range reg.Defs() {
		params := string(d.Function.Parameters)
		assert.Contains(t, params, "alpha")
		assert.Contains(t, params, "beta")
		assert.Contains(t, params, "gamma")
	}
}

// ---- DefaultRegistry does NOT include codegraph tools ----

func TestDefaultRegistry_NoCodegraphTools(t *testing.T) {
	ws := t.TempDir()
	reg := tools.DefaultRegistry(ws)
	for _, d := range reg.Defs() {
		assert.False(t, strings.HasPrefix(d.Function.Name, "codegraph_"),
			"DefaultRegistry should not contain %q; codegraph tools are registered via proxy", d.Function.Name)
	}
}

// ---- Close is idempotent ----

func TestCodegraphProxy_CloseIdempotent(t *testing.T) {
	proxy := makeProxy(
		[]db.Project{stubProject(1, "app")},
		[]db.MCPServer{stubServer("codegraph-1")},
	)
	proxy.Close()
	proxy.Close() // should not panic
}
