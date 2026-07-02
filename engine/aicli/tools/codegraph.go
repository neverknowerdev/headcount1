package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/mcp"
)

// CodegraphProxy provides codegraph tools backed by per-project MCP server
// processes. Each project (regular or external) gets its own codegraph stdio
// instance; the proxy lazily spawns and caches them for the lifetime of the
// agent run. Call Close when the run ends to stop all child processes.
//
// All tools accept an optional "project" parameter that is an enum of known
// project names. Omitting it (or passing the current project's name) routes
// to the current project's server.
type CodegraphProxy struct {
	entries        []cgProjectEntry // ordered: current project first
	currentProject *db.Project
	clients        map[int32]mcp.Client // projectID → live MCP client
	mu             sync.Mutex
}

type cgProjectEntry struct {
	project db.Project
	server  db.MCPServer
}

// NewCodegraphProxy creates a proxy for the given set of project–server pairs.
// currentProject may be nil when the task has no associated project.
func NewCodegraphProxy(currentProject *db.Project, pairs []db.CodegraphProjectServer) *CodegraphProxy {
	entries := make([]cgProjectEntry, 0, len(pairs))
	for _, p := range pairs {
		entries = append(entries, cgProjectEntry{project: p.Project, server: p.Server})
	}
	return &CodegraphProxy{
		entries:        entries,
		currentProject: currentProject,
		clients:        make(map[int32]mcp.Client),
	}
}

// RegisterAll adds all codegraph proxy tools to the registry.
func (p *CodegraphProxy) RegisterAll(r *aicli.Registry) {
	r.Register(&cgProxyTool{proxy: p, name: "codegraph_explore", mcpTool: "codegraph_explore",
		desc: "Semantic one-shot exploration of a codegraph-indexed project. Accepts a natural language question or symbol names; returns line-numbered source, call paths, and blast-radius summaries.",
		extraParams: `"query":{"type":"string","description":"Natural language question or symbol names to explore"}`,
		required:    []string{"query"},
	})
	r.Register(&cgProxyTool{proxy: p, name: "codegraph_query", mcpTool: "codegraph_query",
		desc:        "Search for symbols (functions, classes, methods, etc.) in a codegraph-indexed project.",
		extraParams: `"search":{"type":"string","description":"Symbol name or pattern to search for"},"kind":{"type":"string","description":"Filter by symbol kind: function, class, method, interface, struct, etc."},"limit":{"type":"integer","description":"Maximum results (default 20)"}`,
		required:    []string{"search"},
	})
	r.Register(&cgProxyTool{proxy: p, name: "codegraph_callers", mcpTool: "codegraph_callers",
		desc:        "Find all callers of a symbol in a codegraph-indexed project.",
		extraParams: `"symbol":{"type":"string","description":"Symbol name to find callers for"},"limit":{"type":"integer","description":"Maximum results"}`,
		required:    []string{"symbol"},
	})
	r.Register(&cgProxyTool{proxy: p, name: "codegraph_callees", mcpTool: "codegraph_callees",
		desc:        "Find all functions called by a symbol in a codegraph-indexed project.",
		extraParams: `"symbol":{"type":"string","description":"Symbol name to find callees for"},"limit":{"type":"integer","description":"Maximum results"}`,
		required:    []string{"symbol"},
	})
	r.Register(&cgProxyTool{proxy: p, name: "codegraph_impact", mcpTool: "codegraph_impact",
		desc:        "Analyze the blast radius of changing a symbol — shows what code would be affected.",
		extraParams: `"symbol":{"type":"string","description":"Symbol name to analyze impact for"},"depth":{"type":"integer","description":"Dependency levels to follow (default 2)"}`,
		required:    []string{"symbol"},
	})
	r.Register(&cgProxyTool{proxy: p, name: "codegraph_node", mcpTool: "codegraph_node",
		desc:        "Show the source code of a symbol or file, along with its call paths and callers.",
		extraParams: `"symbol":{"type":"string","description":"Symbol name or file path to inspect"}`,
		required:    []string{"symbol"},
	})
	r.Register(&cgProxyTool{proxy: p, name: "codegraph_files", mcpTool: "codegraph_files",
		desc:        "Show the file structure of a codegraph-indexed project with indexing statistics.",
		extraParams: `"path":{"type":"string","description":"Subdirectory to list (optional)"},"max_depth":{"type":"integer","description":"Maximum depth to display"}`,
		required:    []string{},
	})
	r.Register(&cgProxyTool{proxy: p, name: "codegraph_status", mcpTool: "codegraph_status",
		desc:        "Show codegraph indexing status and statistics for a project.",
		extraParams: ``,
		required:    []string{},
	})
}

// Close stops all spawned codegraph MCP server processes.
func (p *CodegraphProxy) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, c := range p.clients {
		c.Close()
		delete(p.clients, id)
	}
}

// projectNames returns the list of project names for the enum schema.
func (p *CodegraphProxy) projectNames() []string {
	names := make([]string, 0, len(p.entries))
	for _, e := range p.entries {
		names = append(names, e.project.Name)
	}
	return names
}

// resolveEntry resolves the target project entry from the "project" parameter.
// An empty name resolves to the current project.
func (p *CodegraphProxy) resolveEntry(name string) (*cgProjectEntry, error) {
	if name == "" && p.currentProject != nil {
		for i := range p.entries {
			if p.entries[i].project.ID == p.currentProject.ID {
				return &p.entries[i], nil
			}
		}
	}
	if name == "" {
		if len(p.entries) == 0 {
			return nil, fmt.Errorf("no codegraph projects available; add a project with a git repository")
		}
		return &p.entries[0], nil
	}
	for i := range p.entries {
		if p.entries[i].project.Name == name {
			return &p.entries[i], nil
		}
	}
	return nil, fmt.Errorf("unknown project %q; available: %s", name, strings.Join(p.projectNames(), ", "))
}

// clientFor returns (or lazily creates) the MCP client for a project.
func (p *CodegraphProxy) clientFor(ctx context.Context, entry *cgProjectEntry) (mcp.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[entry.project.ID]; ok {
		return c, nil
	}
	c, err := mcp.NewClient(entry.server)
	if err != nil {
		return nil, fmt.Errorf("codegraph: start server for %q: %w", entry.project.Name, err)
	}
	// Hard timeout on the MCP handshake so a hung subprocess never freezes the
	// agent run. 30 s is generous; a healthy server responds in milliseconds.
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := c.Initialize(initCtx); err != nil {
		c.Close()
		return nil, fmt.Errorf("codegraph: initialize server for %q: %w", entry.project.Name, err)
	}
	p.clients[entry.project.ID] = c
	return c, nil
}

// ---- cgProxyTool ----

// cgProxyTool is a single codegraph tool backed by the proxy.
type cgProxyTool struct {
	proxy       *CodegraphProxy
	name        string
	mcpTool     string
	desc        string
	extraParams string   // JSON object properties (without outer braces)
	required    []string // required field names
}

func (t *cgProxyTool) Def() aicli.ToolDef {
	projectEnum := buildProjectEnum(t.proxy.projectNames())

	projectProp := fmt.Sprintf(`"project":{"type":"string","description":"Project to query. Omit to use the current project.","enum":%s}`, projectEnum)

	var props string
	if t.extraParams != "" {
		props = t.extraParams + "," + projectProp
	} else {
		props = projectProp
	}

	requiredJSON := "[]"
	if len(t.required) > 0 {
		b, _ := json.Marshal(t.required)
		requiredJSON = string(b)
	}

	params := fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s}`, props, requiredJSON)

	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        t.name,
			Description: t.desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

func (t *cgProxyTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// Extract the "project" field to resolve routing; forward the rest as-is.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return "", err
	}

	var projectName string
	if v, ok := raw["project"]; ok {
		json.Unmarshal(v, &projectName)
		delete(raw, "project") // don't forward to MCP server
	}

	entry, err := t.proxy.resolveEntry(projectName)
	if err != nil {
		return "", err
	}

	client, err := t.proxy.clientFor(ctx, entry)
	if err != nil {
		return "", err
	}

	// Re-marshal the remaining args for the MCP tool call.
	fwdArgs, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}

	// Hard timeout so a wedged codegraph subprocess fails the tool call
	// instead of freezing the whole agent session.
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	result, err := client.CallTool(callCtx, t.mcpTool, fwdArgs)
	if err != nil {
		return "", fmt.Errorf("codegraph (%s): %w", entry.project.Name, err)
	}
	return result, nil
}

// buildProjectEnum returns a JSON array literal for the project names enum.
func buildProjectEnum(names []string) string {
	if len(names) == 0 {
		return `[]`
	}
	b, _ := json.Marshal(names)
	return string(b)
}
