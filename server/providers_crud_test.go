package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"agent-orchestrator/db"
	endpoints "agent-orchestrator/server/controllers"
)

func setupProvidersTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.LLMProvider{}, &db.ProviderPreset{}))
	return database
}

func setupProvidersRouter(t *testing.T, database *gorm.DB) chi.Router {
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Get("/providers", api.ListProviders)
	r.Get("/providers/presets", api.ListProviderPresets)
	r.Post("/providers", api.CreateProvider)
	r.Post("/providers/from-preset", api.CreateProviderFromPreset)
	r.Put("/providers/{id}", api.UpdateProvider)
	r.Delete("/providers/{id}", api.DeleteProvider)
	r.Post("/providers/{id}/rediscover", api.RediscoverProviderModels)
	return withTestUser(t, database, r)
}

func TestProviderCRUD(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(t, database)

	// Create
	payload := map[string]string{
		"name":          "Custom Provider",
		"base_url":      "https://example.com/v1",
		"api_key":       "sk-test",
		"provider_type": "openai",
		"default_model": "gpt-test",
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/providers", bytes.NewReader(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	var created db.LLMProvider
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.True(t, created.Enabled, "newly created providers should default to enabled")
	assert.False(t, created.Builtin)

	// Delete a non-builtin provider succeeds.
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/providers/%d", created.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/providers", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var list []db.LLMProvider
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.Empty(t, list)
}

func TestProvider_CannotDeleteBuiltin(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(t, database)

	q := db.New(database)
	require.NoError(t, q.EnsureBuiltinLLMProvidersForUser(context.Background(), testSeedUserID(t, q)))
	var builtin db.LLMProvider
	require.NoError(t, database.Where("builtin = ?", true).First(&builtin).Error)

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/providers/%d", builtin.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// Still there afterwards.
	var stillThere db.LLMProvider
	assert.NoError(t, database.First(&stillThere, builtin.ID).Error)
}

func TestProvider_ToggleEnabled(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(t, database)

	q := db.New(database)
	require.NoError(t, q.EnsureBuiltinLLMProvidersForUser(context.Background(), testSeedUserID(t, q)))
	var builtin db.LLMProvider
	require.NoError(t, database.Where("builtin = ?", true).First(&builtin).Error)
	assert.True(t, builtin.Enabled, "builtin providers should be seeded enabled")

	// Disable it via the pointer-based partial update.
	payload := map[string]any{"enabled": false}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/providers/%d", builtin.ID), bytes.NewReader(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var updated db.LLMProvider
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.False(t, updated.Enabled)

	// Omitting the field on a subsequent update must not silently re-enable it.
	payload2 := map[string]any{"name": builtin.Name, "base_url": builtin.BaseUrl, "provider_type": builtin.ProviderType}
	b2, _ := json.Marshal(payload2)
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/providers/%d", builtin.ID), bytes.NewReader(b2))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var afterOmit db.LLMProvider
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &afterOmit))
	assert.False(t, afterOmit.Enabled, "omitting 'enabled' in a request must preserve the current value")

	// Re-enable it explicitly.
	payload3 := map[string]any{"enabled": true}
	b3, _ := json.Marshal(payload3)
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/providers/%d", builtin.ID), bytes.NewReader(b3))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var reEnabled db.LLMProvider
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reEnabled))
	assert.True(t, reEnabled.Enabled)
}

// seedTestPreset inserts a ProviderPreset pointed at a local test server —
// standing in for a real preset like MiniMax/OpenCode Go, whose real
// endpoints this test suite has no network access to.
func seedTestPreset(t *testing.T, database *gorm.DB, key, baseURL string) db.ProviderPreset {
	t.Helper()
	q := db.New(database)
	preset := db.ProviderPreset{Key: key, Name: "Test Preset", BaseUrl: baseURL, ProviderType: "openai"}
	require.NoError(t, database.Create(&preset).Error)
	presets, err := q.ListProviderPresets(context.Background())
	require.NoError(t, err)
	for _, p := range presets {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("seeded preset %q not found", key)
	return db.ProviderPreset{}
}

func TestCreateProviderFromPreset_FetchesModelsAndSkipsProbe(t *testing.T) {
	var gotAuth string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "model-b"}, {"id": "model-a"}},
		})
	}))
	defer mock.Close()

	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(t, database)
	seedTestPreset(t, database, "test-preset", mock.URL)

	payload := map[string]string{"preset_key": "test-preset", "api_key": "sk-test-key"}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/providers/from-preset", bytes.NewReader(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created db.LLMProvider
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.Equal(t, "Bearer sk-test-key", gotAuth, "the models fetch itself is the only validation — no separate probe call")
	assert.Equal(t, "test-preset", created.PresetKey)
	assert.Equal(t, "model-a", created.DefaultModel)
	assert.Equal(t, "model-a,model-b", created.SupportedModels)
	assert.True(t, created.Enabled)
	assert.False(t, created.Builtin)
}

func TestCreateProviderFromPreset_MissingApiKeyRejected(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(t, database)
	seedTestPreset(t, database, "test-preset", "https://example.com")

	payload := map[string]string{"preset_key": "test-preset", "api_key": ""}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/providers/from-preset", bytes.NewReader(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateProviderFromPreset_FailedFetchDoesNotCreateProvider(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mock.Close()

	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(t, database)
	seedTestPreset(t, database, "test-preset", mock.URL)

	payload := map[string]string{"preset_key": "test-preset", "api_key": "sk-bad-key"}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/providers/from-preset", bytes.NewReader(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadGateway, w.Code)

	q := db.New(database)
	all, err := q.ListLLMProviders(context.Background())
	require.NoError(t, err)
	assert.Empty(t, all, "a failed model fetch must not leave behind a half-configured provider")
}

func TestRediscoverProviderModels_PresetProviderUsesSavedApiKey(t *testing.T) {
	var gotAuth string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "new-model"}},
		})
	}))
	defer mock.Close()

	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(t, database)
	q := db.New(database)
	uid := testSeedUserID(t, q)
	provider, err := q.CreateLLMProvider(context.Background(), db.LLMProvider{
		Name: "Test Preset", BaseUrl: mock.URL, ApiKey: "sk-saved-key",
		ProviderType: "openai", PresetKey: "test-preset", Enabled: true, UserID: &uid,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/providers/%d/rediscover", provider.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "Bearer sk-saved-key", gotAuth)
	var updated db.LLMProvider
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.Equal(t, "new-model", updated.DefaultModel)
}

func TestRediscoverProviderModels_PresetProviderWithoutApiKeyRejected(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(t, database)
	q := db.New(database)
	uid := testSeedUserID(t, q)
	provider, err := q.CreateLLMProvider(context.Background(), db.LLMProvider{
		Name: "Test Preset", BaseUrl: "https://example.com", ProviderType: "openai",
		PresetKey: "test-preset", Enabled: true, UserID: &uid,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/providers/%d/rediscover", provider.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListProviderPresets(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(t, database)
	seedTestPreset(t, database, "test-preset", "https://example.com")

	req := httptest.NewRequest(http.MethodGet, "/providers/presets", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var presets []db.ProviderPreset
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &presets))
	require.Len(t, presets, 1)
	assert.Equal(t, "test-preset", presets[0].Key)
}
