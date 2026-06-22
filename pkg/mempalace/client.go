// Package mempalace provides a Go client for the MemPalace MCP server.
// It wraps the engine/mcp stdio client to expose typed methods for diary,
// drawer, and search operations used by the pre/post-run hooks in the engine.
package mempalace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/mcp"
)

// Client wraps an MCP stdio connection to a mempalace-mcp subprocess.
type Client struct {
	inner mcp.Client
}

// DefaultPalaceDir returns the palace directory, overridable via MEMPALACE_PALACE.
func DefaultPalaceDir() string {
	if d := os.Getenv("MEMPALACE_PALACE"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return home + "/.mempalace/palace"
}

// NewClient starts mempalace-mcp using DefaultPalaceDir.
// Reads MEMPALACE_BACKEND to override the storage backend (e.g. "sqlite_exact"
// for testing). Returns nil, nil when mempalace-mcp is not installed so callers
// degrade gracefully without treating an absent binary as an error.
func NewClient(ctx context.Context) (*Client, error) {
	return NewClientWithPalace(ctx, DefaultPalaceDir(), os.Getenv("MEMPALACE_BACKEND"))
}

// NewClientWithPalace starts mempalace-mcp pointing at a specific palace
// directory. backend may be "sqlite_exact", "chromadb", etc., or "" to use
// MemPalace's auto-detected default (chromadb when installed).
func NewClientWithPalace(ctx context.Context, palace, backend string) (*Client, error) {
	if _, err := exec.LookPath("mempalace-mcp"); err != nil {
		return nil, nil // not installed — degrade gracefully
	}

	args := []string{"--palace", palace}
	if backend != "" {
		args = append(args, "--backend", backend)
	}
	argsJSON, _ := json.Marshal(args)

	srv := db.MCPServer{
		Name:      "mempalace",
		Transport: "stdio",
		Command:   "mempalace-mcp",
		Args:      string(argsJSON),
	}

	inner, err := mcp.NewClient(srv)
	if err != nil {
		return nil, fmt.Errorf("mempalace: start process: %w", err)
	}
	if _, err := inner.Initialize(ctx); err != nil {
		inner.Close()
		return nil, fmt.Errorf("mempalace: initialize: %w", err)
	}
	return &Client{inner: inner}, nil
}

// DiaryWrite saves a timestamped entry to the named agent's diary wing.
// wing scopes the entry to a project (e.g. company.ShortName); topic is a
// short category label (e.g. "task-completion", "bug-found").
func (c *Client) DiaryWrite(ctx context.Context, agentName, entry, wing, topic string) error {
	args := map[string]any{
		"agent_name": strings.ToLower(agentName),
		"entry":      entry,
		"wing":       wing,
		"topic":      topic,
	}
	raw, _ := json.Marshal(args)
	_, err := c.inner.CallTool(ctx, "mempalace_diary_write", raw)
	return err
}

// DiaryRead retrieves the last n diary entries for the named agent.
// Passing wing="" returns entries across all wings the agent has written to.
func (c *Client) DiaryRead(ctx context.Context, agentName, wing string, lastN int) (string, error) {
	args := map[string]any{
		"agent_name": strings.ToLower(agentName),
		"last_n":     lastN,
		"wing":       wing,
	}
	raw, _ := json.Marshal(args)
	return c.inner.CallTool(ctx, "mempalace_diary_read", raw)
}

// AddDrawer stores verbatim content in the palace under wing/room.
func (c *Client) AddDrawer(ctx context.Context, wing, room, content string) error {
	args := map[string]any{
		"wing":    wing,
		"room":    room,
		"content": content,
	}
	raw, _ := json.Marshal(args)
	_, err := c.inner.CallTool(ctx, "mempalace_add_drawer", raw)
	return err
}

// Search performs semantic search across the palace.
// wing scopes the search to a specific project; pass "" to search everywhere.
func (c *Client) Search(ctx context.Context, query, wing string, limit int) (string, error) {
	args := map[string]any{
		"query": query,
		"wing":  wing,
		"limit": limit,
	}
	raw, _ := json.Marshal(args)
	return c.inner.CallTool(ctx, "mempalace_search", raw)
}

// GetTaxonomy returns the full palace hierarchy (wings → rooms → drawer counts).
func (c *Client) GetTaxonomy(ctx context.Context) (string, error) {
	raw, _ := json.Marshal(map[string]any{})
	return c.inner.CallTool(ctx, "mempalace_get_taxonomy", raw)
}

// ListDrawers returns drawers filtered by wing and/or room with pagination.
func (c *Client) ListDrawers(ctx context.Context, wing, room string, limit, offset int) (string, error) {
	args := map[string]any{"limit": limit, "offset": offset}
	if wing != "" {
		args["wing"] = wing
	}
	if room != "" {
		args["room"] = room
	}
	raw, _ := json.Marshal(args)
	return c.inner.CallTool(ctx, "mempalace_list_drawers", raw)
}

// GetDrawer fetches a single drawer by ID.
func (c *Client) GetDrawer(ctx context.Context, drawerID string) (string, error) {
	args := map[string]any{"drawer_id": drawerID}
	raw, _ := json.Marshal(args)
	return c.inner.CallTool(ctx, "mempalace_get_drawer", raw)
}

// Close shuts down the underlying MCP subprocess.
func (c *Client) Close() error {
	return c.inner.Close()
}

// hasResults returns true when a MemPalace JSON response contains non-empty results.
func HasResults(response string) bool {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		return strings.TrimSpace(response) != ""
	}
	if results, ok := parsed["results"].([]any); ok {
		return len(results) > 0
	}
	if entries, ok := parsed["entries"].([]any); ok {
		return len(entries) > 0
	}
	return false
}
