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
