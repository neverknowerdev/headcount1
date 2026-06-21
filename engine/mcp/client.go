package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/db"
)



// Client is the interface all MCP transport implementations must satisfy.
type Client interface {
	// Initialize performs the MCP handshake. Must be called before any other method.
	Initialize(ctx context.Context) (*InitializeResult, error)
	// ListTools returns all tools exposed by this MCP server.
	ListTools(ctx context.Context) ([]Tool, error)
	// CallTool invokes a named tool with JSON arguments and returns the result text.
	CallTool(ctx context.Context, name string, args json.RawMessage) (string, error)
	// Close frees any resources (e.g., stops a spawned subprocess).
	Close() error
}

// NewClient constructs the appropriate Client for the given MCPServer config.
func NewClient(s db.MCPServer) (Client, error) {
	switch s.Transport {
	case "stdio":
		if s.Command == "" {
			return nil, fmt.Errorf("mcp server %q: stdio transport requires a command", s.Name)
		}
		var args []string
		if s.Args != "" {
			if err := json.Unmarshal([]byte(s.Args), &args); err != nil {
				return nil, fmt.Errorf("mcp server %q: invalid args JSON: %w", s.Name, err)
			}
		}
		var extraEnv []string
		if s.AuthEnvVar != "" && s.AuthToken != "" {
			extraEnv = []string{s.AuthEnvVar + "=" + s.AuthToken}
		}
		return newStdioClient(s.Command, args, extraEnv)
	case "http":
		if s.URL == "" {
			return nil, fmt.Errorf("mcp server %q: http transport requires a URL", s.Name)
		}
		var headers map[string]string
		if s.Headers != "" {
			if err := json.Unmarshal([]byte(s.Headers), &headers); err != nil {
				return nil, fmt.Errorf("mcp server %q: invalid headers JSON: %w", s.Name, err)
			}
		}
		if s.AuthType == "bearer" && s.AuthToken != "" {
			if headers == nil {
				headers = make(map[string]string)
			}
			headers["Authorization"] = "Bearer " + s.AuthToken
		}
		return newHTTPClient(s.URL, headers), nil
	case "builtin":
		return nil, fmt.Errorf("builtin MCP servers are not accessed via Client")
	default:
		return nil, fmt.Errorf("mcp server %q: unknown transport %q", s.Name, s.Transport)
	}
}

// ExtractText concatenates all text content items from a CallToolResult.
func ExtractText(r *CallToolResult) string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}
