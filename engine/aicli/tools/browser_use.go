package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"agent-orchestrator/engine/aicli"

	"github.com/chromedp/chromedp"
)

// BrowserUse provides headless Chromium browser automation for agents.
// A single Chromium instance is lazily started on first use and reused
// for the lifetime of the tool within a single agent run.
type BrowserUse struct {
	mu         sync.Mutex
	taskCtx    context.Context
	taskCancel context.CancelFunc
}

// NewBrowserUse creates a BrowserUse tool.
func NewBrowserUse() *BrowserUse {
	return &BrowserUse{}
}

func (b *BrowserUse) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: string(ToolBrowserUse),
			Description: "Interact with web pages using a headless browser that executes JavaScript. " +
				"Unlike web_fetch, this renders pages fully including JS-generated content. " +
				"Call 'navigate' first to load a page, then use other actions on the loaded page.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"action": {
						"type": "string",
						"enum": ["navigate", "click", "type", "get_text", "get_html", "screenshot", "execute_js"],
						"description": "Action to perform: navigate (load URL and return page text), click (click CSS selector), type (type text into selector), get_text (extract text from selector), get_html (get HTML of selector), screenshot (capture visible page as base64 PNG), execute_js (run JavaScript and return result)"
					},
					"url": {
						"type": "string",
						"description": "URL to navigate to. Required for 'navigate' action."
					},
					"selector": {
						"type": "string",
						"description": "CSS selector targeting one or more elements. Required for click, type, get_text, get_html. Defaults to 'body' for get_text and get_html."
					},
					"text": {
						"type": "string",
						"description": "Text to type into the element. Required for 'type' action."
					},
					"script": {
						"type": "string",
						"description": "JavaScript expression to evaluate. Its return value is JSON-serialised and returned. Required for 'execute_js' action."
					}
				},
				"required": ["action"]
			}`),
		},
	}
}

func (b *BrowserUse) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Action   string `json:"action"`
		URL      string `json:"url"`
		Selector string `json:"selector"`
		Text     string `json:"text"`
		Script   string `json:"script"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	switch p.Action {
	case "navigate":
		if p.URL == "" {
			return "", fmt.Errorf("browser_use navigate: 'url' is required")
		}
		return b.navigate(ctx, p.URL)
	case "click":
		if p.Selector == "" {
			return "", fmt.Errorf("browser_use click: 'selector' is required")
		}
		return b.click(ctx, p.Selector)
	case "type":
		if p.Selector == "" || p.Text == "" {
			return "", fmt.Errorf("browser_use type: 'selector' and 'text' are required")
		}
		return b.typeText(ctx, p.Selector, p.Text)
	case "get_text":
		sel := p.Selector
		if sel == "" {
			sel = "body"
		}
		return b.getText(ctx, sel)
	case "get_html":
		sel := p.Selector
		if sel == "" {
			sel = "html"
		}
		return b.getHTML(ctx, sel)
	case "screenshot":
		return b.screenshot(ctx)
	case "execute_js":
		if p.Script == "" {
			return "", fmt.Errorf("browser_use execute_js: 'script' is required")
		}
		return b.executeJS(ctx, p.Script)
	default:
		return "", fmt.Errorf("browser_use: unknown action %q", p.Action)
	}
}

// ensureBrowser lazily initialises a headless Chromium instance and returns
// its cdp context. The instance is reused across calls within the same run.
func (b *BrowserUse) ensureBrowser(ctx context.Context) (context.Context, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.taskCtx != nil {
		select {
		case <-b.taskCtx.Done():
			// Browser died or run ended; reset so we recreate on next call.
			b.taskCtx = nil
			b.taskCancel = nil
		default:
			return b.taskCtx, nil
		}
	}

	var opts []chromedp.ExecAllocatorOption
	opts = append(opts, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.NoSandbox)

	if bin := findChromium(); bin != "" {
		opts = append(opts, chromedp.ExecPath(bin))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	taskCtx, taskCancel := chromedp.NewContext(allocCtx)

	// Start the browser process eagerly so failures surface here. chromedp
	// binds the browser's lifetime to the context of this first Run, so it
	// must be taskCtx itself — the launch is bounded with a timer instead of
	// a derived timeout context.
	startDone := make(chan error, 1)
	go func() { startDone <- chromedp.Run(taskCtx) }()
	var startErr error
	select {
	case startErr = <-startDone:
	case <-time.After(30 * time.Second):
		startErr = fmt.Errorf("timed out after 30s")
	}
	if startErr != nil {
		taskCancel()
		allocCancel()
		return nil, fmt.Errorf("browser_use: failed to start browser: %w", startErr)
	}

	b.taskCtx = taskCtx
	b.taskCancel = func() {
		taskCancel()
		allocCancel()
	}
	return b.taskCtx, nil
}

// opTimeout bounds every browser operation so a wedged Chrome instance fails
// the call instead of freezing the agent session (the agent-level watchdog
// exempts browser_use because the browser must outlive individual calls).
const opTimeout = 60 * time.Second

// runOps executes chromedp actions against the shared browser context with a
// per-operation timeout.
func runOps(bc context.Context, actions ...chromedp.Action) error {
	opCtx, cancel := context.WithTimeout(bc, opTimeout)
	defer cancel()
	return chromedp.Run(opCtx, actions...)
}

func (b *BrowserUse) navigate(ctx context.Context, url string) (string, error) {
	bc, err := b.ensureBrowser(ctx)
	if err != nil {
		return "", err
	}
	var text string
	if err := runOps(bc,
		chromedp.Navigate(url),
		chromedp.WaitVisible("body"),
		chromedp.Text("body", &text),
	); err != nil {
		return "", fmt.Errorf("browser_use navigate %s: %w", url, err)
	}
	if len(text) > 50_000 {
		text = text[:50_000] + "\n...(truncated)"
	}
	return fmt.Sprintf("Navigated to %s\n\nPage text content:\n%s", url, text), nil
}

func (b *BrowserUse) click(ctx context.Context, selector string) (string, error) {
	bc, err := b.ensureBrowser(ctx)
	if err != nil {
		return "", err
	}
	if err := runOps(bc, chromedp.Click(selector, chromedp.NodeVisible)); err != nil {
		return "", fmt.Errorf("browser_use click %q: %w", selector, err)
	}
	return fmt.Sprintf("Clicked element matching %q", selector), nil
}

func (b *BrowserUse) typeText(ctx context.Context, selector, text string) (string, error) {
	bc, err := b.ensureBrowser(ctx)
	if err != nil {
		return "", err
	}
	if err := runOps(bc, chromedp.SendKeys(selector, text, chromedp.NodeVisible)); err != nil {
		return "", fmt.Errorf("browser_use type %q: %w", selector, err)
	}
	return fmt.Sprintf("Typed into %q", selector), nil
}

func (b *BrowserUse) getText(ctx context.Context, selector string) (string, error) {
	bc, err := b.ensureBrowser(ctx)
	if err != nil {
		return "", err
	}
	var text string
	if err := runOps(bc, chromedp.Text(selector, &text)); err != nil {
		return "", fmt.Errorf("browser_use get_text %q: %w", selector, err)
	}
	return text, nil
}

func (b *BrowserUse) getHTML(ctx context.Context, selector string) (string, error) {
	bc, err := b.ensureBrowser(ctx)
	if err != nil {
		return "", err
	}
	var html string
	if err := runOps(bc, chromedp.OuterHTML(selector, &html)); err != nil {
		return "", fmt.Errorf("browser_use get_html %q: %w", selector, err)
	}
	if len(html) > 50_000 {
		html = html[:50_000] + "\n...(truncated)"
	}
	return html, nil
}

func (b *BrowserUse) screenshot(ctx context.Context) (string, error) {
	bc, err := b.ensureBrowser(ctx)
	if err != nil {
		return "", err
	}
	var buf []byte
	if err := runOps(bc, chromedp.CaptureScreenshot(&buf)); err != nil {
		return "", fmt.Errorf("browser_use screenshot: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(buf)
	return fmt.Sprintf("Screenshot captured (%d bytes PNG, base64-encoded):\n%s", len(buf), encoded), nil
}

func (b *BrowserUse) executeJS(ctx context.Context, script string) (string, error) {
	bc, err := b.ensureBrowser(ctx)
	if err != nil {
		return "", err
	}
	var result any
	if err := runOps(bc, chromedp.Evaluate(script, &result)); err != nil {
		return "", fmt.Errorf("browser_use execute_js: %w", err)
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

// findChromium searches common locations for a Chromium binary, preferring
// the Playwright-managed installation.
func findChromium() string {
	patterns := []string{
		"/opt/pw-browsers/chromium-*/chrome-linux/chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/usr/bin/google-chrome",
		"/usr/local/bin/chromium",
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}
