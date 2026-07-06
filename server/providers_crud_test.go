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
	require.NoError(t, database.AutoMigrate(&db.LLMProvider{}))
	return database
}

func setupProvidersRouter(database *gorm.DB) chi.Router {
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Get("/providers", api.ListProviders)
	r.Post("/providers", api.CreateProvider)
	r.Put("/providers/{id}", api.UpdateProvider)
	r.Delete("/providers/{id}", api.DeleteProvider)
	return r
}

func TestProviderCRUD(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(database)

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
	r := setupProvidersRouter(database)

	q := db.New(database)
	require.NoError(t, q.EnsureBuiltinLLMProviders(context.Background()))
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
	r := setupProvidersRouter(database)

	q := db.New(database)
	require.NoError(t, q.EnsureBuiltinLLMProviders(context.Background()))
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
