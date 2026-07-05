// Package llmdiscovery discovers the free-tier models currently offered by
// the builtin LLM gateways (OpenRouter, OpenCode Zen) and keeps their DB rows
// in sync. Both gateways are OpenAI-compatible, so no new LLM client is
// needed — this package only fetches and filters their public model catalogs.
package llmdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"agent-orchestrator/db"
)

// fallbackOpenRouterFreeModels is used when the live catalog can't be
// fetched (offline first boot, transient outage), so the provider still has
// a usable model list instead of an empty one. It self-heals: the next
// successful RefreshBuiltinProviderModels call replaces it with live data.
var fallbackOpenRouterFreeModels = []string{
	"deepseek/deepseek-r1:free",
	"deepseek/deepseek-chat-v3.1:free",
	"meta-llama/llama-3.3-70b-instruct:free",
	"google/gemma-3-27b-it:free",
	"qwen/qwen3-coder:free",
	"openai/gpt-oss-120b:free",
}

// fallbackOpenCodeZenFreeModels mirrors fallbackOpenRouterFreeModels for
// OpenCode Zen.
var fallbackOpenCodeZenFreeModels = []string{
	"big-pickle",
	"minimax-m2.5-free",
	"mimo-v2-pro-free",
	"mimo-v2-omni-free",
	"nemotron-3-super-free",
}

// openCodeZenKnownFree lists OpenCode Zen model IDs known to be free even
// though they don't follow the "-free" naming convention.
var openCodeZenKnownFree = map[string]bool{
	"big-pickle": true,
}

type openRouterModel struct {
	ID      string `json:"id"`
	Pricing struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

type openRouterModelsResponse struct {
	Data []openRouterModel `json:"data"`
}

type openAIModel struct {
	ID string `json:"id"`
}

type openAIModelsResponse struct {
	Data []openAIModel `json:"data"`
}

// isOpenRouterFree reports whether a model is free to use: OpenRouter marks
// free models both with a ":free" ID suffix and with "0" prompt/completion
// pricing; checking both makes the filter robust to either signal changing.
func isOpenRouterFree(m openRouterModel) bool {
	if strings.HasSuffix(m.ID, ":free") {
		return true
	}
	return m.Pricing.Prompt == "0" && m.Pricing.Completion == "0"
}

// FetchOpenRouterFreeModels fetches OpenRouter's public model catalog and
// returns the IDs of models that cost nothing to use.
func FetchOpenRouterFreeModels(ctx context.Context, client *http.Client) ([]string, error) {
	var resp openRouterModelsResponse
	if err := fetchJSONWithRetry(ctx, client, db.OpenRouterBaseURL+"/models", &resp); err != nil {
		return nil, fmt.Errorf("fetch OpenRouter models: %w", err)
	}
	var free []string
	for _, m := range resp.Data {
		if isOpenRouterFree(m) {
			free = append(free, m.ID)
		}
	}
	sort.Strings(free)
	return free, nil
}

// isOpenCodeZenFree reports whether a model ID looks free. OpenCode Zen's
// /v1/models endpoint carries no pricing metadata (it's a plain OpenAI-shaped
// model list), so free models are identified by their naming convention
// (an "-free" suffix) plus a small allowlist of known no-suffix free models.
func isOpenCodeZenFree(id string) bool {
	lower := strings.ToLower(id)
	return strings.Contains(lower, "free") || openCodeZenKnownFree[lower]
}

// FetchOpenCodeZenFreeModels fetches OpenCode Zen's public model catalog and
// returns the IDs of models that are free to use.
func FetchOpenCodeZenFreeModels(ctx context.Context, client *http.Client) ([]string, error) {
	var resp openAIModelsResponse
	if err := fetchJSONWithRetry(ctx, client, db.OpenCodeZenBaseURL+"/models", &resp); err != nil {
		return nil, fmt.Errorf("fetch OpenCode Zen models: %w", err)
	}
	var free []string
	for _, m := range resp.Data {
		if isOpenCodeZenFree(m.ID) {
			free = append(free, m.ID)
		}
	}
	sort.Strings(free)
	return free, nil
}

// RefreshBuiltinProviderModels fetches the current free-model catalog for
// each builtin provider row and stores it via UpdateLLMProviderModelCatalog.
// Safe to call repeatedly (startup, or a periodic ticker): a failed fetch
// falls back to a small curated model list rather than leaving the provider
// without any usable models, and never overwrites a DefaultModel the user
// already picked. Returns an error only to log context; callers should treat
// it as a warning, not a startup blocker.
func RefreshBuiltinProviderModels(ctx context.Context, q *db.Queries, client *http.Client) error {
	providers, err := q.ListLLMProviders(ctx)
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}

	var errs []string
	for _, p := range providers {
		if !p.Builtin {
			continue
		}

		var models []string
		var fetchErr error
		switch p.Name {
		case db.ProviderNameOpenRouter:
			models, fetchErr = FetchOpenRouterFreeModels(ctx, client)
			if fetchErr != nil {
				log.Printf("llmdiscovery: %v — falling back to the built-in free-model list", fetchErr)
				models = fallbackOpenRouterFreeModels
			}
		case db.ProviderNameOpenCodeZen:
			models, fetchErr = FetchOpenCodeZenFreeModels(ctx, client)
			if fetchErr != nil {
				log.Printf("llmdiscovery: %v — falling back to the built-in free-model list", fetchErr)
				models = fallbackOpenCodeZenFreeModels
			}
		default:
			continue
		}

		if len(models) == 0 {
			continue
		}
		if err := q.UpdateLLMProviderModelCatalog(ctx, p.ID, models); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p.Name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("llmdiscovery: %s", strings.Join(errs, "; "))
	}
	return nil
}

// retryableStatusError signals an HTTP-level failure worth retrying (429,
// 5xx) as opposed to a hard client error (400, 401, 404, ...).
type retryableStatusError struct {
	status int
	body   string
}

func (e *retryableStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

// retryBaseDelay is the base for exponential backoff between fetch retries;
// overridden by tests to keep them fast.
var retryBaseDelay = time.Second

const fetchMaxAttempts = 3

// fetchJSONWithRetry GETs url and decodes the JSON body into out, retrying
// transient failures (network errors, 429, 5xx) with exponential backoff.
// Non-transient failures (4xx other than 429, malformed JSON) fail fast.
func fetchJSONWithRetry(ctx context.Context, client *http.Client, url string, out interface{}) error {
	var lastErr error
	for attempt := 0; attempt < fetchMaxAttempts; attempt++ {
		if attempt > 0 {
			wait := retryBaseDelay << uint(attempt-1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}

		err := doFetchJSON(ctx, client, url, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryable(err) {
			return err
		}
	}
	return fmt.Errorf("all retries exhausted: %w", lastErr)
}

func isRetryable(err error) bool {
	var rse *retryableStatusError
	if errors.As(err, &rse) {
		return true
	}
	var ue *url.Error
	return errors.As(err, &ue)
}

func doFetchJSON(ctx context.Context, client *http.Client, reqURL string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return &retryableStatusError{status: resp.StatusCode, body: truncate(string(body), 300)}
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("GET %s: HTTP %d: %s", reqURL, resp.StatusCode, truncate(string(body), 300))
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GET %s: decode response: %w", reqURL, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
