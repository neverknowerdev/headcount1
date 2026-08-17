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

// Unregister removes a transient control-plane tool after its activation
// window.
func (r *Registry) Unregister(name string) { delete(r.tools, name) }

func (r *Registry) DefsByName(name string) ToolDef {
	if tool, ok := r.tools[name]; ok {
		return tool.Def()
	}
	return ToolDef{Type: "function", Function: FuncMeta{Name: name}}
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

// Exclude returns a new Registry without the named tools. Unlike Filter, an
// empty exclusion set is still meaningful to callers because it preserves the
// full registry unchanged.
func (r *Registry) Exclude(denied []string) *Registry {
	if len(denied) == 0 {
		return r
	}
	filtered := NewRegistry()
	for name, tool := range r.tools {
		if !nameMatchesFilter(name, denied) {
			filtered.Register(tool)
		}
	}
	return filtered
}

// Names returns the registered tool names in stable order. It is used for
// diagnostics and effective-permission logging.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// nameMatchesFilter reports whether a tool name matches any filter entry.
// An entry ending in "*" matches by prefix (e.g. "codegraph_*").
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

// PromptListing renders the registry as a system-prompt section so the prompt
// always describes the tools that are actually registered. Agent prompt files
// may mention tools in prose; this listing is the authoritative set, generated
// after all registration and filtering, so drift between prompt text and the
// real tool registry can never mislead the model.
func (r *Registry) PromptListing() string {
	if len(r.tools) == 0 {
		return ""
	}
	names := r.Names()

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
