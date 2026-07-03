package aicli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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
func (r *Registry) Filter(allowed []string) *Registry {
	if len(allowed) == 0 {
		return r
	}
	for _, a := range allowed {
		if a == "*" {
			return r
		}
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = true
	}
	filtered := NewRegistry()
	for name, tool := range r.tools {
		if allowedSet[name] {
			filtered.Register(tool)
		}
	}
	return filtered
}

// PromptListing renders the registry as a system-prompt section so the prompt
// always describes the tools that are actually registered. Agent prompt files
// may mention tools in prose; this listing is the authoritative set, generated
// after all registration and filtering, so drift between prompt text and the
// real tool registry can never mislead the model.
func (r *Registry) PromptListing() string {
	if len(r.tools) == 0 {
		return ""
	}
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("\n\n## Available tools\n\n")
	b.WriteString("These are the ONLY tools you can call. If this prompt mentions a tool that is not listed here, it is unavailable — use the closest listed tool instead.\n\n")
	for _, name := range names {
		fmt.Fprintf(&b, "- %s — %s\n", name, firstSentence(r.tools[name].Def().Function.Description))
	}
	return b.String()
}

// firstSentence returns the first sentence of s, capped at 140 characters.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if runes := []rune(s); len(runes) > 140 {
		s = string(runes[:140]) + "…"
	}
	return s
}
