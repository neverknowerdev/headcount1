package llmdiscovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func init() {
	// Keep retry backoff effectively instant in tests.
	retryBaseDelay = time.Millisecond
}

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.LLMProvider{}))
	return database
}

func TestFetchOpenRouterFreeModels_FiltersFreePricing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "openai/gpt-4o", "pricing": map[string]string{"prompt": "0.000005", "completion": "0.000015"}},
				{"id": "deepseek/deepseek-r1:free", "pricing": map[string]string{"prompt": "0", "completion": "0"}},
				{"id": "qwen/qwen3-coder:free", "pricing": map[string]string{"prompt": "0", "completion": "0"}},
				{"id": "meta-llama/llama-guard", "pricing": map[string]string{"prompt": "0", "completion": "0"}},
			},
		})
	}))
	defer srv.Close()

	models, err := fetchOpenRouterFreeModelsFromURL(t, srv.URL+"/models")
	require.NoError(t, err)
	assert.Equal(t, []string{"deepseek/deepseek-r1:free", "meta-llama/llama-guard", "qwen/qwen3-coder:free"}, models)
}

// fetchOpenRouterFreeModelsFromURL exercises the same parsing/filtering logic
// as FetchOpenRouterFreeModels against an arbitrary URL, since the real
// function targets the fixed OpenRouter base URL.
func fetchOpenRouterFreeModelsFromURL(t *testing.T, url string) ([]string, error) {
	t.Helper()
	var resp openRouterModelsResponse
	if err := fetchJSONWithRetry(context.Background(), http.DefaultClient, url, &resp); err != nil {
		return nil, err
	}
	var free []string
	for _, m := range resp.Data {
		if isOpenRouterFree(m) {
			free = append(free, m.ID)
		}
	}
	return sortedCopy(free), nil
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func TestFetchOpenCodeZenFreeModels_FiltersByNameHeuristic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-4o"},
				{"id": "big-pickle"},
				{"id": "minimax-m2.5-free"},
				{"id": "claude-sonnet-4"},
			},
		})
	}))
	defer srv.Close()

	var resp openAIModelsResponse
	require.NoError(t, fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL+"/models", &resp))
	var free []string
	for _, m := range resp.Data {
		if isOpenCodeZenFree(m.ID) {
			free = append(free, m.ID)
		}
	}
	assert.ElementsMatch(t, []string{"big-pickle", "minimax-m2.5-free"}, free)
}

func TestFetchJSONWithRetry_RetriesOn5xxThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "ok"}}})
	}))
	defer srv.Close()

	var resp openAIModelsResponse
	err := fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL, &resp)
	require.NoError(t, err)
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls))
	assert.Equal(t, "ok", resp.Data[0].ID)
}

func TestFetchJSONWithRetry_RetriesOn429ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "ok"}}})
	}))
	defer srv.Close()

	var resp openAIModelsResponse
	err := fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL, &resp)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestFetchJSONWithRetry_DoesNotRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var resp openAIModelsResponse
	err := fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL, &resp)
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
	assert.Contains(t, err.Error(), "404")
}

func TestFetchJSONWithRetry_ExhaustsRetriesAndReturnsError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var resp openAIModelsResponse
	err := fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL, &resp)
	require.Error(t, err)
	assert.Equal(t, int32(fetchMaxAttempts), atomic.LoadInt32(&calls))
	assert.Contains(t, err.Error(), "all retries exhausted")
}

func TestFetchJSONWithRetry_RetriesOnNetworkError(t *testing.T) {
	// A server that closes the connection is a robust way to simulate a
	// transient network error without relying on unroutable addresses (which
	// can hang for a long time in sandboxed environments).
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, err := hj.Hijack()
			require.NoError(t, err)
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "ok"}}})
	}))
	defer srv.Close()

	var resp openAIModelsResponse
	err := fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL, &resp)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestFetchJSONWithRetry_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// Use a real (non-overridden) backoff so cancellation during the wait is
	// exercised, but bound the test with a short timeout.
	origDelay := retryBaseDelay
	retryBaseDelay = 200 * time.Millisecond
	defer func() { retryBaseDelay = origDelay }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var resp openAIModelsResponse
	start := time.Now()
	err := fetchJSONWithRetry(ctx, http.DefaultClient, srv.URL, &resp)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, time.Second, "should abort during backoff wait, not run all retries")
}

func TestUpdateLLMProviderModelCatalog_SetsDefaultOnlyWhenEmpty(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	p, err := q.CreateLLMProvider(ctx, db.LLMProvider{Name: "OpenRouter", BaseUrl: db.OpenRouterBaseURL, Builtin: true})
	require.NoError(t, err)

	require.NoError(t, q.UpdateLLMProviderModelCatalog(ctx, p.ID, []string{"model-a", "model-b"}))
	updated, err := q.GetLLMProvider(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "model-a,model-b", updated.SupportedModels)
	assert.Equal(t, "model-a", updated.DefaultModel)

	// A second refresh with a different catalog must not clobber the
	// already-set (possibly user-chosen) default model.
	require.NoError(t, q.UpdateLLMProviderModelCatalog(ctx, p.ID, []string{"model-c"}))
	updated2, err := q.GetLLMProvider(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "model-c", updated2.SupportedModels)
	assert.Equal(t, "model-a", updated2.DefaultModel, "default model must not be overwritten once set")
}

func TestUpdateLLMProviderModelCatalog_EmptyListIsNoop(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	p, err := q.CreateLLMProvider(ctx, db.LLMProvider{Name: "OpenRouter", BaseUrl: db.OpenRouterBaseURL, Builtin: true, SupportedModels: "existing", DefaultModel: "existing"})
	require.NoError(t, err)

	require.NoError(t, q.UpdateLLMProviderModelCatalog(ctx, p.ID, nil))
	updated, err := q.GetLLMProvider(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "existing", updated.SupportedModels)
	assert.Equal(t, "existing", updated.DefaultModel)
}

func TestForceUpdateLLMProviderModelCatalog_AlwaysOverwritesDefault(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	p, err := q.CreateLLMProvider(ctx, db.LLMProvider{Name: "OpenRouter", BaseUrl: db.OpenRouterBaseURL, Builtin: true, SupportedModels: "old-a,old-b", DefaultModel: "old-a"})
	require.NoError(t, err)

	require.NoError(t, q.ForceUpdateLLMProviderModelCatalog(ctx, p.ID, []string{"new-a", "new-b"}))
	updated, err := q.GetLLMProvider(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "new-a,new-b", updated.SupportedModels)
	assert.Equal(t, "new-a", updated.DefaultModel, "unlike UpdateLLMProviderModelCatalog, this must always overwrite the default")
}

func TestForceUpdateLLMProviderModelCatalog_EmptyListIsNoop(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	p, err := q.CreateLLMProvider(ctx, db.LLMProvider{Name: "OpenRouter", BaseUrl: db.OpenRouterBaseURL, Builtin: true, SupportedModels: "existing", DefaultModel: "existing"})
	require.NoError(t, err)

	require.NoError(t, q.ForceUpdateLLMProviderModelCatalog(ctx, p.ID, nil))
	updated, err := q.GetLLMProvider(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "existing", updated.SupportedModels)
	assert.Equal(t, "existing", updated.DefaultModel)
}

func TestSortByPriority(t *testing.T) {
	priority := []string{"best", "second-best", "third-best"}

	t.Run("all ranked, sorted by priority order", func(t *testing.T) {
		got := sortByPriority([]string{"third-best", "best", "second-best"}, priority)
		assert.Equal(t, []string{"best", "second-best", "third-best"}, got)
	})

	t.Run("unranked models appended alphabetically after ranked ones", func(t *testing.T) {
		got := sortByPriority([]string{"zzz-unranked", "third-best", "aaa-unranked", "best"}, priority)
		assert.Equal(t, []string{"best", "third-best", "aaa-unranked", "zzz-unranked"}, got)
	})

	t.Run("no ranked matches falls back to alphabetical", func(t *testing.T) {
		got := sortByPriority([]string{"c", "a", "b"}, priority)
		assert.Equal(t, []string{"a", "b", "c"}, got)
	})

	t.Run("empty input", func(t *testing.T) {
		assert.Empty(t, sortByPriority(nil, priority))
	})

	t.Run("does not mutate the input slice", func(t *testing.T) {
		input := []string{"third-best", "best"}
		_ = sortByPriority(input, priority)
		assert.Equal(t, []string{"third-best", "best"}, input, "sortByPriority must operate on a copy")
	})
}

func TestEnsureBuiltinLLMProviders_SeedsBothAndIsIdempotent(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	require.NoError(t, q.EnsureBuiltinLLMProviders(ctx))
	providers, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)
	require.Len(t, providers, 2)

	names := map[string]bool{}
	for _, p := range providers {
		names[p.Name] = true
		assert.True(t, p.Builtin)
		assert.Empty(t, p.ApiKey, "builtin providers must not ship with a baked-in API key")
	}
	assert.True(t, names[db.ProviderNameOpenRouter])
	assert.True(t, names[db.ProviderNameOpenCodeZen])

	// Simulate a user having customized the OpenRouter row (own API key/base
	// URL) — calling Ensure again must not clobber it.
	openRouter := providers[0]
	if openRouter.Name != db.ProviderNameOpenRouter {
		openRouter = providers[1]
	}
	openRouter.ApiKey = "user-key"
	openRouter.BaseUrl = "https://custom.example.com/v1"
	_, err = q.UpdateLLMProvider(ctx, openRouter)
	require.NoError(t, err)

	require.NoError(t, q.EnsureBuiltinLLMProviders(ctx))
	after, err := q.GetLLMProvider(ctx, openRouter.ID)
	require.NoError(t, err)
	assert.Equal(t, "user-key", after.ApiKey)
	assert.Equal(t, "https://custom.example.com/v1", after.BaseUrl)

	providersAfter, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)
	assert.Len(t, providersAfter, 2, "re-running Ensure must not create duplicates")
}

func TestRefreshBuiltinProviderModels_FallsBackOnFetchFailure(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()
	require.NoError(t, q.EnsureBuiltinLLMProviders(ctx))

	// Point both builtin providers at a base URL that always 404s, forcing
	// the fetch to fail and the fallback list to be used instead. We can't
	// override the package-level base URL constants, so this test verifies
	// the fallback path indirectly: RefreshBuiltinProviderModels must
	// gracefully populate a non-empty catalog even when the live host used in
	// tests (still openrouter.ai/opencode.ai — unreachable in sandboxed CI)
	// cannot be reached.
	client := &http.Client{Timeout: 2 * time.Second, Transport: alwaysFailTransport{}}
	err := RefreshBuiltinProviderModels(ctx, q, client)
	require.NoError(t, err, "a fetch failure must be absorbed by the fallback list, not surfaced as an error")

	providers, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)
	for _, p := range providers {
		assert.NotEmpty(t, p.SupportedModels, "%s should have a fallback model list", p.Name)
		assert.NotEmpty(t, p.DefaultModel, "%s should have a fallback default model", p.Name)
		if p.Name == db.ProviderNameOpenRouter {
			assert.True(t, strings.Contains(p.SupportedModels, ":free"))
		}
	}
}

func TestSeedFallbackModels_PopulatesBuiltinProvidersWithNoNetwork(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()
	require.NoError(t, q.EnsureBuiltinLLMProviders(ctx))

	// Sanity check: right after seeding, both providers are blank — this is
	// exactly the window SeedFallbackModels exists to close.
	before, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)
	for _, p := range before {
		assert.Empty(t, p.DefaultModel, "%s should start blank before SeedFallbackModels", p.Name)
	}

	require.NoError(t, SeedFallbackModels(ctx, q))

	after, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)
	names := map[string]bool{}
	for _, p := range after {
		names[p.Name] = true
		assert.NotEmpty(t, p.DefaultModel, "%s should have a fallback default model", p.Name)
		assert.NotEmpty(t, p.SupportedModels, "%s should have a fallback model list", p.Name)
	}
	assert.True(t, names[db.ProviderNameOpenRouter])
	assert.True(t, names[db.ProviderNameOpenCodeZen])
}

// TestWipeThenSeedFallback_NeverLeavesABlankDefaultModel is a regression test
// for a real bug: the e2e WipeDB handler re-seeds builtin providers (blank
// catalog by design — no network at wipe time) *after* the one-time,
// process-lifetime RefreshBuiltinProviderModels goroutine has already run.
// Without a synchronous fallback seed, a provider wiped after that point
// would have a permanently blank DefaultModel for the rest of the process's
// life, breaking anything that assumes a builtin provider has one (e.g. the
// "existing provider" onboarding step's required Model Name field silently
// blocking form submission).
func TestWipeThenSeedFallback_NeverLeavesABlankDefaultModel(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	// Simulate real startup: seed + a (here, always-failing) live refresh
	// that still leaves a usable catalog via its own fallback path.
	require.NoError(t, q.EnsureBuiltinLLMProviders(ctx))
	client := &http.Client{Timeout: 2 * time.Second, Transport: alwaysFailTransport{}}
	require.NoError(t, RefreshBuiltinProviderModels(ctx, q, client))

	// Simulate a wipe: providers table cleared and re-seeded blank, exactly
	// like server/controllers/e2e.go's WipeDB does. The one-time discovery
	// goroutine has already run and will never run again for this process.
	require.NoError(t, database.Exec("DELETE FROM llm_providers").Error)
	require.NoError(t, q.EnsureBuiltinLLMProviders(ctx))

	afterWipeOnly, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)
	for _, p := range afterWipeOnly {
		assert.Empty(t, p.DefaultModel, "sanity check: a bare re-seed after wipe must be blank")
	}

	// The fix: WipeDB also calls SeedFallbackModels synchronously.
	require.NoError(t, SeedFallbackModels(ctx, q))

	afterFallbackSeed, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)
	for _, p := range afterFallbackSeed {
		assert.NotEmpty(t, p.DefaultModel, "%s must never be left with a blank DefaultModel after a wipe", p.Name)
		assert.NotEmpty(t, p.SupportedModels, "%s must never be left with a blank SupportedModels after a wipe", p.Name)
	}
}

// alwaysFailTransport simulates total network unreachability (as this
// sandboxed test environment has for openrouter.ai/opencode.ai) so
// RefreshBuiltinProviderModels's fallback path can be exercised without
// depending on real internet access. http.Client.Do wraps whatever error a
// RoundTripper returns in a *url.Error, which is exactly the network-error
// shape isRetryable treats as transient.
type alwaysFailTransport struct{}

func (alwaysFailTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errUnreachable{}
}

type errUnreachable struct{}

func (errUnreachable) Error() string { return "simulated: host unreachable" }
