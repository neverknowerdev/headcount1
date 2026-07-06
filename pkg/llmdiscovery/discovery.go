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
//
// This list doubles as the curated ranking reference in sortByPriority: it's
// ordered best-known-capability first. Neither OpenRouter nor OpenCode Zen
// expose a public "intelligence" or popularity score via their /models
// endpoint, so there's no live signal to sort live-fetched results by —
// this ordering is an editorial judgment call, kept in one place so both the
// offline fallback and the live-fetch ranking stay consistent. Worth
// revisiting periodically as new models show up.
var fallbackOpenRouterFreeModels = []string{
	"openai/gpt-oss-120b:free",
	"deepseek/deepseek-r1:free",
	"deepseek/deepseek-chat-v3.1:free",
	"qwen/qwen3-coder:free",
	"meta-llama/llama-3.3-70b-instruct:free",
	"google/gemma-3-27b-it:free",
}

// fallbackOpenCodeZenFreeModels mirrors fallbackOpenRouterFreeModels for
// OpenCode Zen — also doubles as its curated ranking reference.
var fallbackOpenCodeZenFreeModels = []string{
	"deepseek-v4-flash-free",
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
	if err := fetchJSONWithRetry(ctx, client, db.OpenRouterBaseURL+"/models", "", &resp); err != nil {
		return nil, fmt.Errorf("fetch OpenRouter models: %w", err)
	}
	var free []string
	for _, m := range resp.Data {
		if isOpenRouterFree(m) {
			free = append(free, m.ID)
		}
	}
	return sortByPriority(free, fallbackOpenRouterFreeModels), nil
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
	if err := fetchJSONWithRetry(ctx, client, db.OpenCodeZenBaseURL+"/models", "", &resp); err != nil {
		return nil, fmt.Errorf("fetch OpenCode Zen models: %w", err)
	}
	var free []string
	for _, m := range resp.Data {
		if isOpenCodeZenFree(m.ID) {
			free = append(free, m.ID)
		}
	}
	return sortByPriority(free, fallbackOpenCodeZenFreeModels), nil
}

// PresetDiscoverer knows how to fetch the live model catalog for a provider
// created from a db.ProviderPreset (OpenCode Go, MiniMax, ...), given the
// user's own API key. Unlike the free builtin fetchers above, these presets
// are ordinary paid providers: there's no "free" subset to filter down to,
// and the endpoint requires auth. Most presets share the standard
// OpenAI-compatible /models shape (genericPresetDiscoverer); a preset whose
// endpoint deviates from that can register its own implementation in
// presetDiscoverers instead of special-casing FetchModelsForPreset.
type PresetDiscoverer interface {
	FetchModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error)
}

type genericPresetDiscoverer struct{}

func (genericPresetDiscoverer) FetchModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error) {
	if apiKey == "" {
		return nil, errors.New("an API key is required to discover this provider's models")
	}
	var resp openAIModelsResponse
	if err := fetchJSONWithRetry(ctx, client, baseURL+"/models", apiKey, &resp); err != nil {
		return nil, fmt.Errorf("fetch models: %w", err)
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// presetDiscoverers maps a ProviderPreset's Key to the discoverer that knows
// how to fetch its catalog. New presets default to genericPresetDiscoverer —
// only add an entry here if a preset's /models endpoint needs bespoke
// handling.
var presetDiscoverers = map[string]PresetDiscoverer{
	db.ProviderPresetOpenCodeGo: genericPresetDiscoverer{},
	db.ProviderPresetMiniMax:    genericPresetDiscoverer{},
}

// FetchModelsForPreset fetches the model catalog for a provider created from
// a preset, dispatching to whichever PresetDiscoverer is registered for
// presetKey (falling back to the generic OpenAI-compatible fetch for
// presets that don't need special handling).
func FetchModelsForPreset(ctx context.Context, client *http.Client, presetKey, baseURL, apiKey string) ([]string, error) {
	d, ok := presetDiscoverers[presetKey]
	if !ok {
		d = genericPresetDiscoverer{}
	}
	return d.FetchModels(ctx, client, baseURL, apiKey)
}

// sortByPriority orders ids so that models appearing in priority come first,
// in priority's own order (best first); any id not in priority is appended
// afterward, sorted alphabetically for determinism. This is how "best model
// first" is approximated in the absence of a live ranking signal from either
// provider's API — see the fallback list comments above.
func sortByPriority(ids []string, priority []string) []string {
	rank := make(map[string]int, len(priority))
	for i, id := range priority {
		rank[id] = i
	}
	sorted := append([]string(nil), ids...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, iRanked := rank[sorted[i]]
		rj, jRanked := rank[sorted[j]]
		switch {
		case iRanked && jRanked:
			return ri < rj
		case iRanked && !jRanked:
			return true
		case !iRanked && jRanked:
			return false
		default:
			return sorted[i] < sorted[j]
		}
	})
	return sorted
}

// fallbackModelsFor returns the curated fallback model list for a builtin
// provider name, or nil if the name isn't one of the builtin providers.
func fallbackModelsFor(providerName string) []string {
	switch providerName {
	case db.ProviderNameOpenRouter:
		return fallbackOpenRouterFreeModels
	case db.ProviderNameOpenCodeZen:
		return fallbackOpenCodeZenFreeModels
	default:
		return nil
	}
}

// SeedFallbackModels immediately populates every builtin provider's model
// catalog from the static fallback list — no network call, so it's safe to
// run synchronously right after EnsureBuiltinLLMProviders. This closes the
// window where a builtin provider row exists but has a blank DefaultModel
// (which breaks anything that assumes a provider has a usable default,
// e.g. the "existing provider" onboarding step): without it, a provider
// stays blank until the async RefreshBuiltinProviderModels fetch completes,
// and if something re-seeds the providers afterward (e.g. the e2e WipeDB
// helper, which intentionally skips the network fetch to stay fast), that
// window never closes for the rest of the process's life. The later live
// discovery fetch simply overwrites this seed with fresher data once it
// completes.
func SeedFallbackModels(ctx context.Context, q *db.Queries) error {
	providers, err := q.ListLLMProviders(ctx)
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	for _, p := range providers {
		if !p.Builtin {
			continue
		}
		models := fallbackModelsFor(p.Name)
		if len(models) == 0 {
			continue
		}
		if err := q.UpdateLLMProviderModelCatalog(ctx, p.ID, models); err != nil {
			return fmt.Errorf("%s: %w", p.Name, err)
		}
	}
	return nil
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
		case db.ProviderNameOpenCodeZen:
			models, fetchErr = FetchOpenCodeZenFreeModels(ctx, client)
		default:
			continue
		}
		if fetchErr != nil {
			log.Printf("llmdiscovery: %v — falling back to the built-in free-model list", fetchErr)
			models = fallbackModelsFor(p.Name)
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

// StartDailyModelRefreshScheduler re-runs RefreshBuiltinProviderModels every
// 24 hours so the free-model catalog (and its ranking) stays current without
// requiring a server restart. Run in a goroutine — blocks until ctx is
// cancelled.
func StartDailyModelRefreshScheduler(ctx context.Context, q *db.Queries, client *http.Client) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			log.Println("llmdiscovery: running scheduled model catalog refresh...")
			if err := RefreshBuiltinProviderModels(ctx, q, client); err != nil {
				log.Printf("llmdiscovery: scheduled refresh failed: %v", err)
			}
		case <-ctx.Done():
			return
		}
	}
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
// apiKey is sent as a Bearer token when non-empty; the free-catalog fetchers
// (OpenRouter, OpenCode Zen) call this with an empty key since their public
// /models endpoints need no auth.
func fetchJSONWithRetry(ctx context.Context, client *http.Client, url string, apiKey string, out interface{}) error {
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

		err := doFetchJSON(ctx, client, url, apiKey, out)
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

func doFetchJSON(ctx context.Context, client *http.Client, reqURL string, apiKey string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

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
