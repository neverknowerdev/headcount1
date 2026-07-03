package aicli

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool is the interface every agent tool must implement.
type Tool interface {
	Def() ToolDef
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds all tools available to the agent and dispatches executions.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Def().Function.Name] = t
}

// Defs returns the ToolDef slice for inclusion in a ChatRequest.
func (r *Registry) Defs() []ToolDef {
	defs := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Def())
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

// Filter returns a new Registry containing only tools whose names appear in
// allowed. An empty allowed list or one containing "*" returns r unchanged.
// Entries ending in "*" match by prefix (e.g. "codegraph_*").
func (r *Registry) Filter(allowed []string) *Registry {
	if len(allowed) == 0 {
		return r
	}
	for _, a := range allowed {
		if a == "*" {
			return r
		}
	}
	filtered := NewRegistry()
	for name, tool := range r.tools {
		if nameMatchesFilter(name, allowed) {
			filtered.Register(tool)
		}
	}
	return filtered
}

// nameMatchesFilter reports whether a tool name matches any filter entry.
// An entry ending in "*" matches by prefix.
func nameMatchesFilter(name string, allowed []string) bool {
	for _, a := range allowed {
		if a == name {
			return true
		}
		if n := len(a); n > 0 && a[n-1] == '*' && len(name) >= n-1 && name[:n-1] == a[:n-1] {
			return true
		}
	}
	return false
}
