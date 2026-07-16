package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// httpClient communicates with an MCP server over Streamable HTTP (JSON-RPC over POST).
// It tracks the Mcp-Session-Id returned by initialize and sends it on all subsequent requests.
type httpClient struct {
	url       string
	headers   map[string]string
	http      *http.Client
	nextID    atomic.Int32
	sessionID string
	mu        sync.Mutex
}

func newHTTPClient(url string, headers map[string]string) *httpClient {
	return &httpClient{
		url:     url,
		headers: headers,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *httpClient) post(ctx context.Context, method string, params any) (*Response, error) {
	id := int(c.nextID.Add(1))

	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		paramsRaw = b
	}

	reqBody, err := json.Marshal(Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  paramsRaw,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	c.mu.Lock()
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	c.mu.Unlock()

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http mcp: %w", err)
	}
	defer resp.Body.Close()

	// Capture session ID from the response (Streamable HTTP protocol).
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("http mcp: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http mcp: server returned %d: %s", resp.StatusCode, body)
	}

	var rpcResp Response
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("http mcp: decode response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}
	return &rpcResp, nil
}

func (c *httpClient) Initialize(ctx context.Context) (*InitializeResult, error) {
	params := InitializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    map[string]any{},
		ClientInfo:      ClientInfo{Name: "headcount1", Version: "1.0.0"},
	}
	resp, err := c.post(ctx, "initialize", params)
	if err != nil {
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	var result InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp initialize: decode: %w", err)
	}
	return &result, nil
}

func (c *httpClient) ListTools(ctx context.Context) ([]Tool, error) {
	resp, err := c.post(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	var result ListToolsResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp tools/list: decode: %w", err)
	}
	return result.Tools, nil
}

func (c *httpClient) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	resp, err := c.post(ctx, "tools/call", CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("mcp tools/call %q: %w", name, err)
	}
	var result CallToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("mcp tools/call %q: decode: %w", name, err)
	}
	if result.IsError {
		return "", fmt.Errorf("mcp tool %q error: %s", name, ExtractText(&result))
	}
	return ExtractText(&result), nil
}

func (c *httpClient) Close() error { return nil }
