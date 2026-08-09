package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/pkg/setup"
)

// WebFetch fetches the content of a URL, optionally converting HTML to Markdown.
type WebFetch struct{}

// NewWebFetch creates a WebFetch tool.
func NewWebFetch() *WebFetch {
	return &WebFetch{}
}

func (t *WebFetch) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: string(ToolWebFetch),
			Description: "Fetch the content of a URL. By default converts HTML to clean Markdown " +
				"using markitdown so the LLM can read it without HTML noise. " +
				"Set to_markdown=false to receive the raw response body instead.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {
						"type": "string",
						"description": "HTTP or HTTPS URL to fetch"
					},
					"to_markdown": {
						"type": "boolean",
						"description": "Convert the HTML response to Markdown (default: true). Set to false to receive the raw body.",
						"default": true
					}
				},
				"required": ["url"]
			}`),
		},
	}
}

func (t *WebFetch) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		URL        string `json:"url"`
		ToMarkdown *bool  `json:"to_markdown"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	// Default to_markdown = true when the caller omits the field.
	toMarkdown := p.ToMarkdown == nil || *p.ToMarkdown

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", p.URL, nil)
	if err != nil {
		return "", fmt.Errorf("web_fetch: invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", "headcount1-agent/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50_000))
	if err != nil {
		return "", fmt.Errorf("web_fetch: read body: %w", err)
	}

	if !toMarkdown {
		return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, string(body)), nil
	}

	if !setup.MarkitdownAvailable() {
		return fmt.Sprintf("HTTP %d\n(markdown conversion unavailable — markitdown not installed)\n%s", resp.StatusCode, string(body)), nil
	}

	md, err := htmlToMarkdown(ctx, body)
	if err != nil {
		// Fall back to raw body so the agent still gets something useful.
		return fmt.Sprintf("HTTP %d\n(markdown conversion failed: %v)\n%s", resp.StatusCode, err, string(body)), nil
	}
	return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, md), nil
}

// htmlToMarkdown converts HTML bytes to Markdown via Microsoft's markitdown
// Python library, invoked as a subprocess.
func htmlToMarkdown(ctx context.Context, html []byte) (string, error) {
	const script = `import sys
from markitdown import MarkItDown
md = MarkItDown()
result = md.convert_stream(sys.stdin.buffer, file_extension='.html')
sys.stdout.write(result.text_content)
`
	cmd := exec.CommandContext(ctx, setup.PythonInterpreter(), "-c", script)
	cmd.Stdin = bytes.NewReader(html)

	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		hint := strings.TrimSpace(stderr.String())
		if hint != "" {
			return "", fmt.Errorf("markitdown: %w — %s", err, hint)
		}
		return "", fmt.Errorf("markitdown: %w", err)
	}

	return strings.TrimSpace(out.String()), nil
}
