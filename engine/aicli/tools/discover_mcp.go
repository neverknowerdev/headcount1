package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/mcp"
)

// DiscoverMCPTools implements the two-phase MCP tool loading.
// When the agent calls discover_mcp_tools(server_name), this tool connects
// to the named MCP server, fetches its tool list, and registers each tool
// in the shared registry so subsequent LLM turns can call them.
type DiscoverMCPTools struct {
	registry *aicli.Registry
	servers  map[string]db.MCPServer // name -> config
}

// NewDiscoverMCPTools creates a DiscoverMCPTools tool.
// registry is the live registry the agent is using; new tools will be added to it.
// servers is the map of available (non-builtin) MCP server configs.
func NewDiscoverMCPTools(registry *aicli.Registry, servers []db.MCPServer) *DiscoverMCPTools {
	m := make(map[string]db.MCPServer, len(servers))
	for _, s := range servers {
		m[s.Name] = s
	}
	return &DiscoverMCPTools{registry: registry, servers: m}
}

func (t *DiscoverMCPTools) Def() aicli.ToolDef {
	names := make([]string, 0, len(t.servers))
	for name := range t.servers {
		names = append(names, name)
	}
	desc := "Discover and load tools from an MCP (Model Context Protocol) server. " +
		"After calling this, the server's tools become available for you to use. " +
		"Available servers: " + strings.Join(names, ", ")
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "discover_mcp_tools",
			Description: desc,
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"server_name":{
						"type":"string",
						"description":"Name of the MCP server to discover tools from"
					}
				},
				"required":["server_name"]
			}`),
		},
	}
}

func (t *DiscoverMCPTools) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ServerName string `json:"server_name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("discover_mcp_tools: %w", err)
	}

	srv, ok := t.servers[p.ServerName]
	if !ok {
		available := make([]string, 0, len(t.servers))
		for name := range t.servers {
			available = append(available, name)
		}
		return "", fmt.Errorf("unknown MCP server %q; available: %s", p.ServerName, strings.Join(available, ", "))
	}

	client, err := mcp.NewClient(srv)
	if err != nil {
		return "", fmt.Errorf("discover_mcp_tools: connect to %q: %w", p.ServerName, err)
	}

	if _, err := client.Initialize(ctx); err != nil {
		client.Close()
		return "", fmt.Errorf("discover_mcp_tools: initialize %q: %w", p.ServerName, err)
	}

	mcpTools, err := client.ListTools(ctx)
	if err != nil {
		client.Close()
		return "", fmt.Errorf("discover_mcp_tools: list tools for %q: %w", p.ServerName, err)
	}

	var registered []string
	for _, tool := range mcpTools {
		adapter := &mcpToolAdapter{
			client:      client,
			tool:        tool,
			serverName:  p.ServerName,
		}
		t.registry.Register(adapter)
		registered = append(registered, tool.Name)
	}

	if len(registered) == 0 {
		client.Close()
		return fmt.Sprintf("MCP server %q has no tools.", p.ServerName), nil
	}

	return fmt.Sprintf("Discovered %d tool(s) from MCP server %q: %s",
		len(registered), p.ServerName, strings.Join(registered, ", ")), nil
}

// mcpToolAdapter wraps an mcp.Tool as an aicli.Tool so it can be registered
// in the agent's registry after discovery.
type mcpToolAdapter struct {
	client     mcp.Client
	tool       mcp.Tool
	serverName string
}

func (a *mcpToolAdapter) Def() aicli.ToolDef {
	schema := a.tool.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        a.tool.Name,
			Description: a.tool.Description,
			Parameters:  schema,
		},
	}
}

func (a *mcpToolAdapter) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	result, err := a.client.CallTool(ctx, a.tool.Name, args)
	if err != nil {
		return "", fmt.Errorf("[%s] %w", a.serverName, err)
	}
	return result, nil
}
