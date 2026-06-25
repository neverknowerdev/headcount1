package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/mcp"
)

// TestStdioClientWithEchoMCPServer spins up a tiny in-process MCP server
// written as a Go helper binary and verifies the full initialize/list/call flow.
// The helper is compiled on the fly using 'go run'.
func TestStdioClientWithEchoMCPServer(t *testing.T) {
	// Check that 'go' is available.
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not in PATH; skipping stdio MCP test")
	}

	// Write the tiny echo MCP server to a temp file.
	helperSrc := `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type msg struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      *int            ` + "`json:\"id,omitempty\"`" + `
	Method  string          ` + "`json:\"method,omitempty\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		var req msg
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		if req.Method == "notifications/initialized" {
			continue
		}
		var result interface{}
		switch req.Method {
		case "initialize":
			result = map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{},
				"serverInfo":      map[string]interface{}{"name": "echo-server", "version": "0.1.0"},
			}
		case "tools/list":
			result = map[string]interface{}{
				"tools": []interface{}{
					map[string]interface{}{
						"name":        "echo",
						"description": "Echo the input text back",
						"inputSchema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"text": map[string]interface{}{"type": "string"},
							},
						},
					},
				},
			}
		case "tools/call":
			var p struct {
				Name      string          ` + "`json:\"name\"`" + `
				Arguments json.RawMessage ` + "`json:\"arguments\"`" + `
			}
			json.Unmarshal(req.Params, &p)
			var args struct{ Text string ` + "`json:\"text\"`" + ` }
			json.Unmarshal(p.Arguments, &args)
			result = map[string]interface{}{
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "echo: " + strings.ToUpper(args.Text)},
				},
			}
		default:
			result = map[string]interface{}{"error": "unknown method"}
		}
		resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}
		b, _ := json.Marshal(resp)
		fmt.Fprintln(os.Stdout, string(b))
	}
}
`

	dir := t.TempDir()
	helperFile := dir + "/echo_mcp.go"
	if err := os.WriteFile(helperFile, []byte(helperSrc), 0644); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	srv := db.MCPServer{
		Name:      "echo",
		Transport: "stdio",
		Command:   "go",
		Args:      `["run", "` + helperFile + `"]`,
	}

	client, err := mcp.NewClient(srv)
	require.NoError(t, err)
	defer client.Close()

	ctx := context.Background()

	// Initialize
	info, err := client.Initialize(ctx)
	require.NoError(t, err)
	assert.Equal(t, "echo-server", info.ServerInfo.Name)

	// List tools
	tools, err := client.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)

	// Call tool
	args, _ := json.Marshal(map[string]string{"text": "hello"})
	result, err := client.CallTool(ctx, "echo", args)
	require.NoError(t, err)
	assert.Equal(t, "echo: HELLO", result)
}

func TestNewClient_InvalidTransport(t *testing.T) {
	srv := db.MCPServer{Name: "test", Transport: "ftp"}
	_, err := mcp.NewClient(srv)
	assert.ErrorContains(t, err, "unknown transport")
}

func TestNewClient_StdioMissingCommand(t *testing.T) {
	srv := db.MCPServer{Name: "test", Transport: "stdio"}
	_, err := mcp.NewClient(srv)
	assert.ErrorContains(t, err, "command")
}

func TestNewClient_HTTPMissingURL(t *testing.T) {
	srv := db.MCPServer{Name: "test", Transport: "http"}
	_, err := mcp.NewClient(srv)
	assert.ErrorContains(t, err, "URL")
}
