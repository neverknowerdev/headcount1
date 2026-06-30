package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

type lineResult struct {
	line string
	err  error
}

// stdioClient communicates with an MCP server via a subprocess's stdin/stdout.
type stdioClient struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	mu      sync.Mutex
	nextID  atomic.Int32
	linesCh chan lineResult // fed by background reader goroutine
	closeOnce sync.Once
	doneCh  chan struct{} // closed to stop the background reader
}

// newStdioClient spawns the MCP server subprocess. extraEnv is a list of
// "KEY=VALUE" strings appended to the current process environment.
// dir sets the working directory of the subprocess; empty string means inherit.
func newStdioClient(command string, args []string, extraEnv []string, dir string) (*stdioClient, error) {
	cmd := exec.Command(command, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	if dir != "" {
		cmd.Dir = dir
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdio mcp: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdio mcp: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("stdio mcp: start %q: %w", command, err)
	}

	c := &stdioClient{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdoutPipe),
		linesCh: make(chan lineResult, 64),
		doneCh:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// readLoop reads lines from the subprocess stdout and forwards them to linesCh.
// It runs for the lifetime of the client and exits when the pipe is closed or
// doneCh is signalled.
func (c *stdioClient) readLoop() {
	defer close(c.linesCh)
	for {
		line, err := c.stdout.ReadString('\n')
		if line != "" {
			select {
			case c.linesCh <- lineResult{line: line}:
			case <-c.doneCh:
				return
			}
		}
		if err != nil {
			select {
			case c.linesCh <- lineResult{err: err}:
			case <-c.doneCh:
			}
			return
		}
	}
}

func (c *stdioClient) send(ctx context.Context, method string, params any) (*Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := int(c.nextID.Add(1))

	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("stdio mcp: marshal params: %w", err)
		}
		paramsRaw = b
	}

	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  paramsRaw,
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("stdio mcp: marshal request: %w", err)
	}
	reqBytes = append(reqBytes, '\n')

	if _, err := c.stdin.Write(reqBytes); err != nil {
		return nil, fmt.Errorf("stdio mcp: write: %w", err)
	}

	// Read from the background reader, respecting context cancellation.
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r, ok := <-c.linesCh:
			if !ok {
				return nil, fmt.Errorf("stdio mcp: reader closed")
			}
			if r.err != nil {
				return nil, fmt.Errorf("stdio mcp: read: %w", r.err)
			}
			var resp Response
			if err := json.Unmarshal([]byte(r.line), &resp); err != nil {
				continue // skip unparseable lines (e.g. server log output)
			}
			if resp.ID != id {
				continue // notification or response for a different request
			}
			if resp.Error != nil {
				return nil, resp.Error
			}
			return &resp, nil
		}
	}
}

func (c *stdioClient) Initialize(ctx context.Context) (*InitializeResult, error) {
	params := InitializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    map[string]any{},
		ClientInfo:      ClientInfo{Name: "paperclip2", Version: "1.0.0"},
	}
	resp, err := c.send(ctx, "initialize", params)
	if err != nil {
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	var result InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp initialize: decode result: %w", err)
	}
	// Send initialized notification (no response expected).
	notif := map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}
	b, _ := json.Marshal(notif)
	b = append(b, '\n')
	c.stdin.Write(b) //nolint:errcheck
	return &result, nil
}

func (c *stdioClient) ListTools(ctx context.Context) ([]Tool, error) {
	resp, err := c.send(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	var result ListToolsResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp tools/list: decode: %w", err)
	}
	return result.Tools, nil
}

func (c *stdioClient) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	params := CallToolParams{Name: name, Arguments: args}
	resp, err := c.send(ctx, "tools/call", params)
	if err != nil {
		return "", fmt.Errorf("mcp tools/call %q: %w", name, err)
	}
	var result CallToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("mcp tools/call %q: decode: %w", name, err)
	}
	if result.IsError {
		return "", fmt.Errorf("mcp tool %q returned error: %s", name, ExtractText(&result))
	}
	return ExtractText(&result), nil
}

func (c *stdioClient) Close() error {
	c.closeOnce.Do(func() {
		close(c.doneCh)
	})
	c.stdin.Close()
	return c.cmd.Wait()
}
