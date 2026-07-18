package llmdiscovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"

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
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.LLMProvider{}))
	return database
}

// testSeedUserID returns (creating if needed) the fixture user builtin
// providers are seeded under, now that builtins are per-user rows.
func testSeedUserID(t *testing.T, q *db.Queries) int32 {
	t.Helper()
	u, err := q.GetUserByEmail(context.Background(), "seed@test.local")
	if err != nil {
		u, err = q.CreateUser(context.Background(), "seed@test.local")
		require.NoError(t, err)
	}
	var dek [32]byte
	dek[0], dek[1] = byte(u.ID), 0x5e
	secrets.Default().UnlockUser(u.ID, dek, time.Hour)
	return u.ID
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
	if err := fetchJSONWithRetry(context.Background(), http.DefaultClient, url, "", &resp); err != nil {
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
	require.NoError(t, fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL+"/models", "", &resp))
	var free []string
	for _, m := range resp.Data {
		if isOpenCodeZenFree(m.ID) {
			free = append(free, m.ID)
		}
	}
	assert.ElementsMatch(t, []string{"big-pickle", "minimax-m2.5-free"}, free)
}

func TestFetchModelsForPreset_RequiresApiKey(t *testing.T) {
	_, err := FetchModelsForPreset(context.Background(), http.DefaultClient, db.ProviderPresetMiniMax, "https://example.com", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key is required")
}

func TestFetchModelsForPreset_SendsBearerAuthAndReturnsFullUnfilteredCatalog(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "MiniMax-M3"},
				{"id": "MiniMax-Text-01"},
			},
		})
	}))
	defer srv.Close()

	models, err := FetchModelsForPreset(context.Background(), http.DefaultClient, db.ProviderPresetMiniMax, srv.URL, "sk-test-key")
	require.NoError(t, err)
	assert.Equal(t, "Bearer sk-test-key", gotAuth)
	// No free/paid filtering for presets — everything the endpoint returns
	// comes back, alphabetized.
	assert.Equal(t, []string{"MiniMax-M3", "MiniMax-Text-01"}, models)
}

func TestFetchModelsForPreset_MiniMaxPrefersM3AsDefaultRegardlessOfAlphabeticalOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "MiniMax-Abab-01"}, // sorts before MiniMax-M3 alphabetically
				{"id": "MiniMax-Text-01"},
				{"id": "MiniMax-M3"},
			},
		})
	}))
	defer srv.Close()

	models, err := FetchModelsForPreset(context.Background(), http.DefaultClient, db.ProviderPresetMiniMax, srv.URL, "sk-test-key")
	require.NoError(t, err)
	require.NotEmpty(t, models)
	assert.Equal(t, "MiniMax-M3", models[0], "MiniMax-M3 should win the default-model slot even though it doesn't sort first alphabetically")
}

func TestFetchModelsForPreset_UnknownKeyFallsBackToGenericDiscoverer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "some-model"}}})
	}))
	defer srv.Close()

	models, err := FetchModelsForPreset(context.Background(), http.DefaultClient, "some-future-preset", srv.URL, "sk-test-key")
	require.NoError(t, err)
	assert.Equal(t, []string{"some-model"}, models)
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
	err := fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL, "", &resp)
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
	err := fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL, "", &resp)
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
	err := fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL, "", &resp)
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
	err := fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL, "", &resp)
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
	err := fetchJSONWithRetry(context.Background(), http.DefaultClient, srv.URL, "", &resp)
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
	err := fetchJSONWithRetry(ctx, http.DefaultClient, srv.URL, "", &resp)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, time.Second, "should abort during backoff wait, not run all retries")
}

func TestUpdateLLMProviderModelCatalog_PreservesDefaultWhileStillValid(t *testing.T) {
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

	// A second refresh where the current default is still on offer (just
	// reordered) must not perturb it.
	require.NoError(t, q.UpdateLLMProviderModelCatalog(ctx, p.ID, []string{"model-b", "model-a"}))
	updated2, err := q.GetLLMProvider(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "model-b,model-a", updated2.SupportedModels)
	assert.Equal(t, "model-a", updated2.DefaultModel, "a still-valid default must not be perturbed")
}

func TestUpdateLLMProviderModelCatalog_RepicksDefaultWhenItDisappears(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	p, err := q.CreateLLMProvider(ctx, db.LLMProvider{Name: "OpenRouter", BaseUrl: db.OpenRouterBaseURL, Builtin: true})
	require.NoError(t, err)

	require.NoError(t, q.UpdateLLMProviderModelCatalog(ctx, p.ID, []string{"model-a", "model-b"}))
	updated, err := q.GetLLMProvider(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "model-a", updated.DefaultModel)

	// The provider stopped offering model-a (renamed/discontinued upstream)
	// — the stale default must be replaced, not left dangling at a model
	// that no longer exists in the catalog.
	require.NoError(t, q.UpdateLLMProviderModelCatalog(ctx, p.ID, []string{"model-c"}))
	updated2, err := q.GetLLMProvider(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "model-c", updated2.SupportedModels)
	assert.Equal(t, "model-c", updated2.DefaultModel, "a default that vanished from the catalog must be re-picked")
}

func TestUpdateLLMProviderModelCatalog_DefaultIsFirstOfCallerOrderedList(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	p, err := q.CreateLLMProvider(ctx, db.LLMProvider{Name: "OpenRouter", BaseUrl: db.OpenRouterBaseURL, Builtin: true})
	require.NoError(t, err)

	// UpdateLLMProviderModelCatalog trusts the caller's ordering (e.g.
	// pkg/llmdiscovery's sortByPriority already put the preferred model
	// first) rather than re-deciding preference itself.
	require.NoError(t, q.UpdateLLMProviderModelCatalog(ctx, p.ID, []string{"openai/gpt-oss-120b:free", "model-a", "model-b"}))
	updated, err := q.GetLLMProvider(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-oss-120b:free", updated.DefaultModel)
}

func TestOpenRouterDefaultPriority_PrefersGptOss120bRegardlessOfAlphabeticalOrder(t *testing.T) {
	// FetchOpenRouterFreeModels always targets the fixed OpenRouter base URL
	// (no live HTTP access in this sandboxed test environment), so exercise
	// the actual ordering it applies — sortByPriority with
	// openRouterDefaultPriority — directly.
	models := sortByPriority([]string{"aardvark/aaa:free", "openai/gpt-oss-120b:free", "zzz/model:free"}, openRouterDefaultPriority)
	require.NotEmpty(t, models)
	assert.Equal(t, "openai/gpt-oss-120b:free", models[0], "gpt-oss-120b should win the default slot even though it doesn't sort first alphabetically")
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

func TestEnsureBuiltinLLMProviders_SeedsBothAndIsIdempotent(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	require.NoError(t, q.EnsureBuiltinLLMProvidersForUser(ctx, testSeedUserID(t, q)))
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

	require.NoError(t, q.EnsureBuiltinLLMProvidersForUser(ctx, testSeedUserID(t, q)))
	after, err := q.GetLLMProvider(ctx, openRouter.ID)
	require.NoError(t, err)
	assert.Equal(t, "user-key", after.ApiKey)
	assert.Equal(t, "https://custom.example.com/v1", after.BaseUrl)

	providersAfter, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)
	assert.Len(t, providersAfter, 2, "re-running Ensure must not create duplicates")
}

func TestEnsureBuiltinLLMProviders_SetsStableProviderName(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	require.NoError(t, q.EnsureBuiltinLLMProvidersForUser(ctx, testSeedUserID(t, q)))
	providers, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)

	byVendor := map[string]bool{}
	for _, p := range providers {
		byVendor[p.ProviderName] = true
	}
	assert.True(t, byVendor[db.ProviderVendorOpenRouter])
	assert.True(t, byVendor[db.ProviderVendorOpenCodeZen])
}

func TestBuiltinProviderDispatch_SurvivesUserRenamingDisplayName(t *testing.T) {
	// Regression test: RediscoverProviderModels/RefreshBuiltinProviderModels
	// used to dispatch by matching the user-editable display Name — renaming
	// a builtin provider (allowed by UpdateProvider) would silently break
	// that dispatch. ProviderName is never touched by UpdateProvider, so it
	// must still identify the provider correctly after a rename.
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()
	require.NoError(t, q.EnsureBuiltinLLMProvidersForUser(ctx, testSeedUserID(t, q)))

	var openRouter db.LLMProvider
	require.NoError(t, database.Where("name = ?", db.ProviderNameOpenRouter).First(&openRouter).Error)

	// Simulate UpdateProvider's field-by-field mutation of an
	// already-fetched row (it never touches ProviderName).
	openRouter.Name = "My Renamed Provider"
	updated, err := q.UpdateLLMProvider(ctx, openRouter)
	require.NoError(t, err)
	assert.Equal(t, db.ProviderVendorOpenRouter, updated.ProviderName, "ProviderName must survive a display-name rename")
}

func TestRefreshBuiltinProviderModels_FetchFailureLeavesCatalogUntouched(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()
	require.NoError(t, q.EnsureBuiltinLLMProvidersForUser(ctx, testSeedUserID(t, q)))

	// Give both providers a known-good catalog first, simulating a prior
	// successful discovery.
	providers, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)
	for _, p := range providers {
		require.NoError(t, q.UpdateLLMProviderModelCatalog(ctx, p.ID, []string{"existing-model"}))
	}

	// A subsequent refresh whose fetch fails entirely (host unreachable) must
	// not touch that already-known-good catalog — there is no hardcoded
	// fallback list to substitute in, so the safest thing is to leave it
	// alone and retry later.
	client := &http.Client{Timeout: 2 * time.Second, Transport: alwaysFailTransport{}}
	err = RefreshBuiltinProviderModels(ctx, q, client)
	require.NoError(t, err, "a fetch failure is logged and skipped, not surfaced as an error")

	after, err := q.ListLLMProviders(ctx)
	require.NoError(t, err)
	for _, p := range after {
		assert.Equal(t, "existing-model", p.SupportedModels, "%s's catalog must be untouched by a failed fetch", p.Name)
		assert.Equal(t, "existing-model", p.DefaultModel, "%s's default must be untouched by a failed fetch", p.Name)
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
