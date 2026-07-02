package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/mcp"
)

// mcpSession holds a live connection to one MCP server.
type mcpSession struct {
	client mcp.Client
	tools  []mcp.Tool
}

// MCPSessionStore manages live MCP connections shared across the MCP tools.
type MCPSessionStore struct {
	mu            sync.Mutex
	sessions      map[string]*mcpSession
	servers       map[string]db.MCPServer
	cachedTools   map[string][]mcp.Tool    // parsed from ToolsCache at startup
	disabledTools map[string]map[string]bool // serverName → toolName → disabled
	onAuthError   func(serverName, errMsg string)
	onToolCall    func(serverName, toolName string)
}

// NewMCPSessionStore creates a session store with the given server configs.
// Tool names are pre-populated from each server's ToolsCache field.
func NewMCPSessionStore(servers []db.MCPServer, onAuthError func(string, string), onToolCall func(string, string)) *MCPSessionStore {
	srvMap := make(map[string]db.MCPServer, len(servers))
	toolCache := make(map[string][]mcp.Tool, len(servers))
	for _, s := range servers {
		srvMap[s.Name] = s
		if s.ToolsCache != "" {
			var cached []mcp.Tool
			if json.Unmarshal([]byte(s.ToolsCache), &cached) == nil {
				toolCache[s.Name] = cached
			}
		}
	}
	return &MCPSessionStore{
		sessions:      make(map[string]*mcpSession),
		servers:       srvMap,
		cachedTools:   toolCache,
		disabledTools: make(map[string]map[string]bool),
		onAuthError:   onAuthError,
		onToolCall:    onToolCall,
	}
}

// AddExternalServer adds an external MCP server to the store after construction.
func (s *MCPSessionStore) AddExternalServer(srv db.MCPServer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.servers[srv.Name] = srv
	if srv.ToolsCache != "" {
		var cached []mcp.Tool
		if json.Unmarshal([]byte(srv.ToolsCache), &cached) == nil {
			s.cachedTools[srv.Name] = cached
		}
	}
}

// SetDisabledTools records which tools are disabled for a given server name.
// Disabled tools are hidden from listings and blocked at call time.
func (s *MCPSessionStore) SetDisabledTools(serverName string, disabled map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabledTools == nil {
		s.disabledTools = make(map[string]map[string]bool)
	}
	s.disabledTools[serverName] = disabled
}

// filterEnabled returns only the tools that are not in the disabled set for this server.
func (s *MCPSessionStore) filterEnabled(serverName string, tools []mcp.Tool) []mcp.Tool {
	disabled, ok := s.disabledTools[serverName]
	if !ok || len(disabled) == 0 {
		return tools
	}
	out := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if !disabled[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// ServerNames returns the sorted list of available server names.
func (s *MCPSessionStore) ServerNames() []string {
	names := make([]string, 0, len(s.servers))
	for name := range s.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ListingCostByServer returns the estimated token cost (chars/4) of each server's
// line in the CompactListing output. Used to attribute MCP listing overhead per server.
func (s *MCPSessionStore) ListingCostByServer() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]int, len(s.servers))
	for name := range s.servers {
		cached := s.filterEnabled(name, s.cachedTools[name])
		var line string
		if len(cached) == 0 {
			line = "* " + name + "\n"
		} else {
			toolNames := make([]string, len(cached))
			for i, t := range cached {
				toolNames[i] = t.Name
			}
			line = "* " + name + ": " + strings.Join(toolNames, ", ") + "\n"
		}
		result[name] = (len(line) + 3) / 4
	}
	return result
}

// CompactListing returns the system-prompt block listing each server and its tool names.
func (s *MCPSessionStore) CompactListing() string {
	names := s.ServerNames()
	if len(names) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nAvailable MCP servers:\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		cached := s.filterEnabled(name, s.cachedTools[name])
		if len(cached) == 0 {
			fmt.Fprintf(&sb, "* %s\n", name)
		} else {
			toolNames := make([]string, len(cached))
			for i, t := range cached {
				toolNames[i] = t.Name
			}
			fmt.Fprintf(&sb, "* %s: %s\n", name, strings.Join(toolNames, ", "))
		}
	}
	sb.WriteString("Use call_mcp_tool(server, tool, input) to invoke a tool. Use discover_mcp_tool(server, tool) for full description and parameters.")
	return sb.String()
}

// connect establishes a fresh MCP connection and caches the session.
func (s *MCPSessionStore) connect(ctx context.Context, serverName string) (*mcpSession, error) {
	srv, ok := s.servers[serverName]
	if !ok {
		return nil, fmt.Errorf("unknown MCP server %q; available: %s",
			serverName, strings.Join(s.ServerNames(), ", "))
	}
	client, err := mcp.NewClient(srv)
	if err != nil {
		return nil, fmt.Errorf("connect to %q: %w", serverName, err)
	}
	// Bound the handshake so a hung server never freezes the agent run.
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := client.Initialize(initCtx); err != nil {
		client.Close()
		return nil, fmt.Errorf("initialize %q: %w", serverName, err)
	}
	mcpTools, err := client.ListTools(initCtx)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("list tools for %q: %w", serverName, err)
	}
	sess := &mcpSession{client: client, tools: mcpTools}
	s.mu.Lock()
	s.sessions[serverName] = sess
	s.cachedTools[serverName] = mcpTools
	s.mu.Unlock()
	return sess, nil
}

// getOrConnect returns an existing session or creates a new one.
func (s *MCPSessionStore) getOrConnect(ctx context.Context, serverName string) (*mcpSession, error) {
	s.mu.Lock()
	sess, ok := s.sessions[serverName]
	s.mu.Unlock()
	if ok {
		return sess, nil
	}
	return s.connect(ctx, serverName)
}

// toolsForServer returns the tool list, using cache or connecting as needed.
// Disabled tools are filtered out before returning.
func (s *MCPSessionStore) toolsForServer(ctx context.Context, serverName string) ([]mcp.Tool, error) {
	s.mu.Lock()
	sess, hasSess := s.sessions[serverName]
	cached, hasCached := s.cachedTools[serverName]
	s.mu.Unlock()
	var tools []mcp.Tool
	if hasSess {
		tools = sess.tools
	} else if hasCached {
		tools = cached
	} else {
		// Nothing in memory; connect to get a fresh list.
		if _, ok := s.servers[serverName]; !ok {
			return nil, fmt.Errorf("unknown MCP server %q", serverName)
		}
		var err error
		sess, err = s.connect(ctx, serverName)
		if err != nil {
			return nil, err
		}
		tools = sess.tools
	}
	s.mu.Lock()
	filtered := s.filterEnabled(serverName, tools)
	s.mu.Unlock()
	return filtered, nil
}

// isMCPAuthError detects authentication failures in MCP tool call errors.
func isMCPAuthError(err error) bool {
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "401") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "bad credentials") ||
		strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "forbidden")
}

// formatToolDescription renders a human-readable description of a tool with its parameters.
func formatToolDescription(tool mcp.Tool) string {
	var sb strings.Builder
	sb.WriteString(tool.Name)
	if tool.Description != "" {
		sb.WriteString(" — ")
		sb.WriteString(tool.Description)
	}
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if len(tool.InputSchema) == 0 || json.Unmarshal(tool.InputSchema, &schema) != nil || len(schema.Properties) == 0 {
		return sb.String()
	}
	req := make(map[string]bool, len(schema.Required))
	for _, r := range schema.Required {
		req[r] = true
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	sb.WriteString("\n\nParameters:")
	for _, name := range names {
		prop := schema.Properties[name]
		suffix := ""
		if !req[name] {
			suffix = " (optional)"
		}
		sb.WriteString(fmt.Sprintf("\n  %s%s: %s", name, suffix, prop.Type))
		if prop.Description != "" {
			sb.WriteString(" — " + prop.Description)
		}
	}
	return sb.String()
}

// ─── Tool 1: call_mcp_tool ───────────────────────────────────────────────────

// CallMCPTool is a single dispatcher that invokes any tool on any discovered server.
type CallMCPTool struct{ store *MCPSessionStore }

func (t *CallMCPTool) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "call_mcp_tool",
			Description: "Invoke a tool on an MCP server.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"server":{"type":"string","description":"MCP server name"},
					"tool":{"type":"string","description":"Tool name to call"},
					"input":{"type":"object","description":"Tool input arguments (pass {} if the tool takes no arguments)"}
				},
				"required":["server","tool"]
			}`),
		},
	}
}

func (t *CallMCPTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Server string          `json:"server"`
		Tool   string          `json:"tool"`
		Input  json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("call_mcp_tool: %w", err)
	}
	if len(p.Input) == 0 {
		p.Input = json.RawMessage("{}")
	}

	// Block calls to disabled tools.
	t.store.mu.Lock()
	disabled := t.store.disabledTools[p.Server]
	t.store.mu.Unlock()
	if disabled[p.Tool] {
		return "", fmt.Errorf("[%s/%s] tool is disabled for this agent", p.Server, p.Tool)
	}

	sess, err := t.store.getOrConnect(ctx, p.Server)
	if err != nil {
		return "", err
	}
	// Hard timeout so a wedged MCP server fails the tool call instead of
	// freezing the whole agent session.
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, callErr := sess.client.CallTool(callCtx, p.Tool, p.Input)
	if callErr != nil {
		if t.store.onAuthError != nil && isMCPAuthError(callErr) {
			t.store.onAuthError(p.Server, callErr.Error())
		}
		return "", fmt.Errorf("[%s/%s] %w", p.Server, p.Tool, callErr)
	}
	if t.store.onToolCall != nil {
		t.store.onToolCall(p.Server, p.Tool)
	}
	return result, nil
}

// ─── Tool 2: discover_mcp_tool ───────────────────────────────────────────────

// DiscoverMCPTool returns the full description and parameters of a specific MCP tool.
type DiscoverMCPTool struct{ store *MCPSessionStore }

func (t *DiscoverMCPTool) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "discover_mcp_tool",
			Description: "Get the full description and parameter details for a specific MCP tool before calling it.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"server":{"type":"string","description":"MCP server name"},
					"tool":{"type":"string","description":"Tool name"}
				},
				"required":["server","tool"]
			}`),
		},
	}
}

func (t *DiscoverMCPTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("discover_mcp_tool: %w", err)
	}

	mcpTools, err := t.store.toolsForServer(ctx, p.Server)
	if err != nil {
		return "", err
	}
	for _, tool := range mcpTools {
		if tool.Name == p.Tool {
			return formatToolDescription(tool), nil
		}
	}
	return "", fmt.Errorf("tool %q not found in server %q", p.Tool, p.Server)
}

// ─── Constructors ─────────────────────────────────────────────────────────────

// NewMCPTools returns the two MCP tools that share a session store.
func NewMCPTools(store *MCPSessionStore) (aicli.Tool, aicli.Tool) {
	return &CallMCPTool{store: store},
		&DiscoverMCPTool{store: store}
}
