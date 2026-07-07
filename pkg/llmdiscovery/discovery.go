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

// openRouterDefaultPriority names OpenRouter's preferred default model —
// a solid general-purpose free model — so it wins the default slot over
// whatever happens to sort first alphabetically. Reuses sortByPriority (see
// below): if this model isn't actually offered, it's simply ignored and the
// catalog stays plain alphabetical. This is the only free-model ID this
// package hardcodes; every other model comes straight from the live fetch.
var openRouterDefaultPriority = []string{"openai/gpt-oss-120b:free"}

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
	return sortByPriority(free, openRouterDefaultPriority), nil
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
	sort.Strings(free)
	return free, nil
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

// presetDefaultPriority optionally names a preset's preferred default
// model(s) — e.g. MiniMax-M3 is MiniMax's current flagship model, so it
// should win over whatever happens to sort first alphabetically. Reuses
// sortByPriority (see below): a model not in the discovered catalog is
// simply ignored, and a preset with no entry here just keeps the
// alphabetical order from FetchModels.
var presetDefaultPriority = map[string][]string{
	db.ProviderPresetMiniMax: {"MiniMax-M3"},
}

// FetchModelsForPreset fetches the model catalog for a provider created from
// a preset, dispatching to whichever PresetDiscoverer is registered for
// presetKey (falling back to the generic OpenAI-compatible fetch for
// presets that don't need special handling), then applies that preset's
// preferred default model ordering, if any.
func FetchModelsForPreset(ctx context.Context, client *http.Client, presetKey, baseURL, apiKey string) ([]string, error) {
	d, ok := presetDiscoverers[presetKey]
	if !ok {
		d = genericPresetDiscoverer{}
	}
	models, err := d.FetchModels(ctx, client, baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	if priority, ok := presetDefaultPriority[presetKey]; ok {
		models = sortByPriority(models, priority)
	}
	return models, nil
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

// RefreshBuiltinProviderModels fetches the current free-model catalog for
// each builtin provider row and stores it via UpdateLLMProviderModelCatalog.
// Safe to call repeatedly (startup, or a periodic ticker). A failed fetch is
// logged and skipped for that provider, leaving its last known-good catalog
// untouched — there is no hardcoded fallback list to fall back to, since a
// stale, hand-maintained list of "free models" can silently drift out of
// sync with what a provider actually offers (a model can be renamed or
// discontinued upstream) and end up worse than just trying again next time.
// Returns an error only to log context; callers should treat it as a
// warning, not a startup blocker.
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
		switch p.ProviderName {
		case db.ProviderVendorOpenRouter:
			models, fetchErr = FetchOpenRouterFreeModels(ctx, client)
		case db.ProviderVendorOpenCodeZen:
			models, fetchErr = FetchOpenCodeZenFreeModels(ctx, client)
		default:
			continue
		}
		if fetchErr != nil {
			log.Printf("llmdiscovery: %v — leaving %s's model catalog untouched", fetchErr, p.Name)
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
			// Exponential backoff: 1x, 2x, 4x, 8x... retryBaseDelay on
			// successive retries (attempt 1, 2, 3, 4...).
			backoffMultiplier := time.Duration(1) << uint(attempt-1)
			wait := retryBaseDelay * backoffMultiplier
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
