// Package aicli provides a direct LLM client and agent loop. It speaks the
// OpenAI-compatible chat-completions API and handles tool calling, retry
// logic, and token-stats extraction in pure Go.
package aicli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is a single entry in the conversation history.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a tool invocation inside an assistant message.
type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function FuncCall `json:"function"`
}

// FuncCall holds the name and JSON-encoded arguments of a tool invocation.
type FuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDef describes a callable tool to the LLM (OpenAI function-calling spec).
type ToolDef struct {
	Type     string   `json:"type"`
	Function FuncMeta `json:"function"`
}

// FuncMeta is the metadata block inside a ToolDef.
type FuncMeta struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatRequest is the body for POST /v1/chat/completions.
type ChatRequest struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	Tools           []ToolDef `json:"tools,omitempty"`
	Stream          bool      `json:"stream,omitempty"`
	MaxTokens       int       `json:"max_tokens,omitempty"`
	// ReasoningEffort controls how much reasoning the model applies.
	// Accepted values: "low", "medium", "high". Supported by OpenAI o-series
	// and compatible providers; ignored by providers that don't support it.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// ChatResponse is parsed from the provider's JSON response body.
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
	// Some providers embed errors in a 200 body.
	Error *APIErr `json:"error,omitempty"`
}

// Choice is one completion alternative returned by the LLM.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage contains the token counts reported by the LLM provider.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// APIErr is the error structure returned by some providers inside a 200 body.
type APIErr struct {
	Message string       `json:"message"`
	Type    string       `json:"type"`
	Code    flexibleCode `json:"code"`
}

func (e *APIErr) Error() string {
	return fmt.Sprintf("LLM error [%s]: %s", e.Type, e.Message)
}

// retryable reports whether an embedded-200-body error describes a
// transient condition (rate limiting, provider overload/5xx) rather than a
// hard failure (bad API key, invalid request, unknown model). Some
// OpenAI-compatible gateways — including free-tier providers like OpenRouter,
// which enforce a request-per-minute cap on free models — report rate limits
// this way instead of (or in addition to) an HTTP 429 status.
func (e *APIErr) retryable() bool {
	t := strings.ToLower(e.Type)
	c := strings.ToLower(string(e.Code))
	return strings.Contains(t, "rate_limit") ||
		strings.Contains(t, "overloaded") ||
		strings.Contains(t, "server_error") ||
		strings.Contains(t, "internal_error") ||
		c == "429" ||
		strings.Contains(c, "rate_limit") ||
		strings.Contains(c, "overloaded")
}

// flexibleCode unmarshals an error "code" field regardless of whether the
// provider sent it as a JSON string (e.g. "rate_limit_exceeded") or a JSON
// number (e.g. 429) — providers are inconsistent here, and a strict string
// field would fail to decode the entire response when a number shows up.
type flexibleCode string

func (c *flexibleCode) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*c = flexibleCode(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*c = flexibleCode(n.String())
		return nil
	}
	// null or some other unexpected shape: leave it empty rather than
	// failing the whole response over a cosmetic error field.
	*c = ""
	return nil
}

// retryableError signals a transient failure that warrants a retry: either
// an HTTP-level failure (429, 5xx) or a network-level one (timeout,
// connection reset, DNS failure — status left at 0).
type retryableError struct {
	status int
	body   string
}

func (e *retryableError) Error() string {
	if e.status == 0 {
		return fmt.Sprintf("network error: %s", e.body)
	}
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

// Client makes non-streaming OpenAI-compatible chat-completion requests.
type Client struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
	MaxRetries int
	// RetryBaseDelay is the base of the exponential backoff between retries:
	// attempt N waits RetryBaseDelay*2^(N-1). Defaults to 1s (see NewClient);
	// a zero value falls back to that default in Complete so a Client built
	// via a bare struct literal (as some tests do) still behaves sanely.
	RetryBaseDelay time.Duration
}

// NewClient creates a Client with sensible defaults.
func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL:        strings.TrimRight(baseURL, "/"),
		APIKey:         apiKey,
		Model:          model,
		HTTPClient:     &http.Client{Timeout: 120 * time.Second},
		MaxRetries:     3,
		RetryBaseDelay: time.Second,
	}
}

// Complete sends a non-streaming chat completion request.
// It retries on transient HTTP errors (429, 5xx) with exponential back-off.
// Returns the parsed response, the raw response body, and any error.
func (c *Client) Complete(ctx context.Context, req ChatRequest) (*ChatResponse, []byte, error) {
	req.Model = c.Model
	req.Stream = false

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}

	retryBase := c.RetryBaseDelay
	if retryBase <= 0 {
		retryBase = time.Second
	}

	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1x, 2x, 4x, 8x... retryBase on successive
			// retries (attempt 1, 2, 3, 4...).
			backoffMultiplier := time.Duration(1) << uint(attempt-1)
			wait := retryBase * backoffMultiplier
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(wait):
			}
		}

		resp, rawBody, err := c.doRequest(ctx, body)
		if err != nil {
			lastErr = err
			var re *retryableError
			if errors.As(err, &re) {
				continue
			}
			return nil, rawBody, err
		}
		return resp, rawBody, nil
	}
	return nil, nil, fmt.Errorf("all retries exhausted: %w", lastErr)
}

// buildChatURL resolves the full chat/completions endpoint from a provider base
// URL. Handles the common case where the stored URL already includes "/v1".
func buildChatURL(baseURL string) string {
	baseURL = strings.TrimSuffix(baseURL, "/")
	const endpoint = "/chat/completions"
	if strings.HasSuffix(baseURL, endpoint) {
		return baseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	return baseURL + "/v1" + endpoint
}

func (c *Client) doRequest(ctx context.Context, body []byte) (*ChatResponse, []byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", buildChatURL(c.BaseURL), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	httpResp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		// A context we were handed was cancelled/timed out deliberately —
		// surface that as-is rather than retrying. Anything else (connection
		// reset, DNS failure, the HTTPClient's own request timeout, ...) is
		// treated as a transient network error worth retrying.
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, &retryableError{body: err.Error()}
	}
	defer httpResp.Body.Close()
	rawBody, _ := io.ReadAll(httpResp.Body)

	// Retry on transient errors.
	if httpResp.StatusCode == http.StatusTooManyRequests || httpResp.StatusCode >= 500 {
		return nil, rawBody, &retryableError{status: httpResp.StatusCode, body: string(rawBody)}
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(rawBody, &chatResp); err != nil {
		return nil, rawBody, fmt.Errorf("unmarshal response (status=%d): %w", httpResp.StatusCode, err)
	}

	// Detect errors embedded in a 200 body (some providers do this, including
	// free-tier rate limits that don't always surface as an HTTP 429).
	if chatResp.Error != nil {
		if chatResp.Error.retryable() {
			return nil, rawBody, &retryableError{status: httpResp.StatusCode, body: chatResp.Error.Error()}
		}
		return nil, rawBody, chatResp.Error
	}

	return &chatResp, rawBody, nil
}
