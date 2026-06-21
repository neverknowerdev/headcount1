package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"agent-orchestrator/engine/aicli/tools"
)

// ---- resolveRepoPath tests ----
// We expose resolveRepoPath via a test helper to keep the env private.

func makeCodegraphInit(t *testing.T) (*tools.CodegraphInit, string) {
	t.Helper()
	ws := t.TempDir()
	return tools.NewCodegraphInit(ws), ws
}

func TestCodegraphInit_Def(t *testing.T) {
	tool, _ := makeCodegraphInit(t)
	def := tool.Def()
	assert.Equal(t, "codegraph_init", def.Function.Name)
	assert.Equal(t, "function", def.Type)
	assert.NotEmpty(t, def.Function.Description)
	assert.NotEmpty(t, def.Function.Parameters)
}

func TestCodegraphQuery_Def(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphQuery(ws)
	def := tool.Def()
	assert.Equal(t, "codegraph_query", def.Function.Name)
	assert.Equal(t, "function", def.Type)
}

func TestCodegraphExplore_Def(t *testing.T) {
	ws := t.TempDir()
	def := tools.NewCodegraphExplore(ws).Def()
	assert.Equal(t, "codegraph_explore", def.Function.Name)
}

func TestCodegraphCallers_Def(t *testing.T) {
	ws := t.TempDir()
	def := tools.NewCodegraphCallers(ws).Def()
	assert.Equal(t, "codegraph_callers", def.Function.Name)
}

func TestCodegraphCallees_Def(t *testing.T) {
	ws := t.TempDir()
	def := tools.NewCodegraphCallees(ws).Def()
	assert.Equal(t, "codegraph_callees", def.Function.Name)
}

func TestCodegraphImpact_Def(t *testing.T) {
	ws := t.TempDir()
	def := tools.NewCodegraphImpact(ws).Def()
	assert.Equal(t, "codegraph_impact", def.Function.Name)
}

func TestCodegraphNode_Def(t *testing.T) {
	ws := t.TempDir()
	def := tools.NewCodegraphNode(ws).Def()
	assert.Equal(t, "codegraph_node", def.Function.Name)
}

func TestCodegraphFiles_Def(t *testing.T) {
	ws := t.TempDir()
	def := tools.NewCodegraphFiles(ws).Def()
	assert.Equal(t, "codegraph_files", def.Function.Name)
}

// ---- Invalid JSON ----

func TestCodegraphInit_InvalidJSON(t *testing.T) {
	tool, _ := makeCodegraphInit(t)
	_, err := tool.Execute(context.Background(), json.RawMessage(`not json`))
	require.Error(t, err)
}

func TestCodegraphQuery_InvalidJSON(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphQuery(ws)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{`))
	require.Error(t, err)
}

func TestCodegraphExplore_InvalidJSON(t *testing.T) {
	ws := t.TempDir()
	_, err := tools.NewCodegraphExplore(ws).Execute(context.Background(), json.RawMessage(`}`))
	require.Error(t, err)
}

func TestCodegraphCallers_InvalidJSON(t *testing.T) {
	ws := t.TempDir()
	_, err := tools.NewCodegraphCallers(ws).Execute(context.Background(), json.RawMessage(`!`))
	require.Error(t, err)
}

func TestCodegraphCallees_InvalidJSON(t *testing.T) {
	ws := t.TempDir()
	_, err := tools.NewCodegraphCallees(ws).Execute(context.Background(), json.RawMessage(`!`))
	require.Error(t, err)
}

func TestCodegraphImpact_InvalidJSON(t *testing.T) {
	ws := t.TempDir()
	_, err := tools.NewCodegraphImpact(ws).Execute(context.Background(), json.RawMessage(`!`))
	require.Error(t, err)
}

func TestCodegraphNode_InvalidJSON(t *testing.T) {
	ws := t.TempDir()
	_, err := tools.NewCodegraphNode(ws).Execute(context.Background(), json.RawMessage(`!`))
	require.Error(t, err)
}

func TestCodegraphFiles_InvalidJSON(t *testing.T) {
	ws := t.TempDir()
	_, err := tools.NewCodegraphFiles(ws).Execute(context.Background(), json.RawMessage(`!`))
	require.Error(t, err)
}

// ---- Not-installed error ----
// When codegraph is not in PATH, tools should return a helpful error.
// We simulate this by running in an environment where codegraph is absent.

func withoutCodegraphInPath(t *testing.T, fn func()) {
	t.Helper()
	orig := os.Getenv("PATH")
	// Set PATH to an empty temp dir so no binaries are found.
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	defer func() { os.Setenv("PATH", orig) }()
	fn()
}

func TestCodegraphQuery_NotInstalled(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphQuery(ws)
	withoutCodegraphInPath(t, func() {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"search":"foo"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "codegraph not found")
	})
}

func TestCodegraphExplore_NotInstalled(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphExplore(ws)
	withoutCodegraphInPath(t, func() {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"how does auth work"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "codegraph not found")
	})
}

func TestCodegraphCallers_NotInstalled(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphCallers(ws)
	withoutCodegraphInPath(t, func() {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"foo"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "codegraph not found")
	})
}

func TestCodegraphImpact_NotInstalled(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphImpact(ws)
	withoutCodegraphInPath(t, func() {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"foo"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "codegraph not found")
	})
}

func TestCodegraphInit_NotInstalled(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphInit(ws)
	withoutCodegraphInPath(t, func() {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "codegraph not found")
	})
}

// ---- Path escape rejection ----

func TestCodegraphQuery_PathEscape(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphQuery(ws)
	withoutCodegraphInPath(t, func() {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"search":"foo","repo_path":"../../etc"}`))
		require.Error(t, err)
		assert.Contains(t, strings.ToLower(err.Error()), "escapes")
	})
}

func TestCodegraphExplore_PathEscape(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphExplore(ws)
	withoutCodegraphInPath(t, func() {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"foo","repo_path":"../../../tmp"}`))
		require.Error(t, err)
		assert.Contains(t, strings.ToLower(err.Error()), "escapes")
	})
}

func TestCodegraphImpact_PathEscape(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphImpact(ws)
	withoutCodegraphInPath(t, func() {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"foo","repo_path":"../../secret"}`))
		require.Error(t, err)
		assert.Contains(t, strings.ToLower(err.Error()), "escapes")
	})
}

// ---- Absolute path outside workspace rejected ----

func TestCodegraphQuery_AbsolutePathOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphQuery(ws)
	withoutCodegraphInPath(t, func() {
		outsidePath := filepath.Join(t.TempDir(), "other")
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"search":"foo","repo_path":"`+outsidePath+`"}`))
		require.Error(t, err)
		assert.Contains(t, strings.ToLower(err.Error()), "outside")
	})
}

// ---- Workspace subdirectory is allowed ----

func TestCodegraphQuery_WorkspaceSubdirAllowed(t *testing.T) {
	ws := t.TempDir()
	sub := filepath.Join(ws, "src")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	tool := tools.NewCodegraphQuery(ws)
	withoutCodegraphInPath(t, func() {
		// Path escapes workspace check happens after PATH lookup;
		// since PATH is empty we get codegraph-not-found, not a path error.
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"search":"foo","repo_path":"src"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "codegraph not found")
	})
}

// ---- DefaultRegistry includes all codegraph tools ----

func TestDefaultRegistry_IncludesCodegraphTools(t *testing.T) {
	ws := t.TempDir()

	reg := tools.DefaultRegistry(ws)
	defs := reg.Defs()

	nameSet := make(map[string]bool, len(defs))
	for _, d := range defs {
		nameSet[d.Function.Name] = true
	}

	for _, name := range []string{
		"codegraph_init",
		"codegraph_query",
		"codegraph_explore",
		"codegraph_callers",
		"codegraph_callees",
		"codegraph_impact",
		"codegraph_node",
		"codegraph_files",
	} {
		assert.True(t, nameSet[name], "DefaultRegistry should contain tool %q", name)
	}
}

// ---- Init with external repo: invalid URL fails gracefully ----

func TestCodegraphInit_ExternalRepo_InvalidURL(t *testing.T) {
	ws := t.TempDir()
	tool := tools.NewCodegraphInit(ws)

	// An invalid git URL should produce a git clone error (not a panic).
	// We override PATH so codegraph isn't found, but git clone runs first.
	// Run with a URL that can't possibly clone.
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"repo_url":"not-a-valid-url-xyz"}`))
	// Either an error or a result containing "failed" — both are acceptable.
	if err != nil {
		assert.Contains(t, strings.ToLower(err.Error()), "clone")
	} else {
		// git clone returned non-zero, which we surface as a result string
		assert.True(t, strings.Contains(strings.ToLower(result), "clone") ||
			strings.Contains(strings.ToLower(result), "error") ||
			strings.Contains(strings.ToLower(result), "failed") ||
			strings.Contains(strings.ToLower(result), "not found"), result)
	}
}
