package aicli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Tool is a callable function that the agent can invoke during its loop.
type Tool struct {
	Def     ToolDef
	Execute func(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds all tools available to the agent and dispatches executions.
type Registry struct {
	tools map[string]*Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Def.Function.Name] = &t
}

// Defs returns the ToolDef slice for inclusion in a ChatRequest.
func (r *Registry) Defs() []ToolDef {
	defs := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Def)
	}
	return defs
}

// Execute runs the named tool with the given JSON arguments.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.Execute(ctx, args)
}

// DefaultTools returns a Registry with the built-in file/shell/web tools pre-loaded.
// All file tools are sandboxed to workspacePath — paths outside it are rejected.
func DefaultTools(workspacePath string) *Registry {
	r := NewRegistry()
	r.Register(readFileTool(workspacePath))
	r.Register(writeFileTool(workspacePath))
	r.Register(listDirTool(workspacePath))
	r.Register(execCommandTool(workspacePath))
	r.Register(grepTool(workspacePath))
	r.Register(webFetchTool())
	return r
}

// resolvePath resolves path relative to workspacePath and verifies the result
// stays inside the workspace (sandbox enforcement). Returns an error if the
// resolved path would escape the workspace boundary.
func resolvePath(workspacePath, path string) (string, error) {
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(filepath.Join(workspacePath, path))
	}
	workspace := filepath.Clean(workspacePath)
	if abs != workspace && !strings.HasPrefix(abs, workspace+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", path)
	}
	return abs, nil
}
