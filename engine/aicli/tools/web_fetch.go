package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"agent-orchestrator/engine/aicli"
)

// WebFetch fetches the content of a URL.
type WebFetch struct{}

// NewWebFetch creates a WebFetch tool.
func NewWebFetch() *WebFetch {
	return &WebFetch{}
}

func (t *WebFetch) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "web_fetch",
			Description: "Fetch the content of a URL. Returns the response body as text (truncated to 50KB).",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"url":{"type":"string","description":"HTTP or HTTPS URL to fetch"}
				},
				"required":["url"]
			}`),
		},
	}
}

func (t *WebFetch) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", p.URL, nil)
	if err != nil {
		return "", fmt.Errorf("web_fetch: invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", "paperclip-agent/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50_000))
	if err != nil {
		return "", fmt.Errorf("web_fetch: read body: %w", err)
	}
	return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, string(body)), nil
}
