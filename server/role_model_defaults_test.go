package server

import (
	"bytes"
	"encoding/json"
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
	t.Setenv("E2E_PAPERCLIP_HOME", t.TempDir())
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
	r.Delete("/providers/{id}", api.DeleteProvider)
	r.Get("/settings", api.GetSettings)
	r.Post("/settings", api.UpdateSettings)
	return r
}

func createProviderReq(t *testing.T, r chi.Router, name, defaultModel string) map[string]interface{} {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{
		"name":          name,
		"base_url":      "http://example.test",
		"api_key":       "key",
		"provider_type": "openai",
		"default_model": defaultModel,
	})
	req := httptest.NewRequest(http.MethodPost, "/providers", bytes.NewReader(payload))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	return out
}

func getSettings(t *testing.T, r chi.Router) endpoints.Settings {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var out endpoints.Settings
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	return out
}

func saveSettings(t *testing.T, r chi.Router, settings endpoints.Settings) {
	t.Helper()
	payload, err := json.Marshal(settings)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/settings", bytes.NewReader(payload))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

// TestCreateProvider_FirstProviderBecomesDefault verifies that creating the
// very first LLM provider in the system automatically becomes the system-wide
// default provider, so no role is ever left unresolvable while a provider
// exists — individual roles stay unset (0) and fall back to it.
func TestCreateProvider_FirstProviderBecomesDefault(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(database)

	created := createProviderReq(t, r, "First Provider", "model-a")
	providerID := int32(created["id"].(float64))

	settings := getSettings(t, r)
	assert.Equal(t, providerID, settings.DefaultProviderID)
	// Individual roles are left unset — they resolve via DefaultProviderID.
	assert.Equal(t, int32(0), settings.RoleModels.SmartPlannerProviderID)
	assert.Equal(t, int32(0), settings.RoleModels.CoderProviderID)
}

// TestCreateProvider_SecondProviderDoesNotOverrideDefault verifies that
// adding an additional provider leaves the existing default provider
// untouched.
func TestCreateProvider_SecondProviderDoesNotOverrideDefault(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(database)

	first := createProviderReq(t, r, "First Provider", "model-a")
	firstID := int32(first["id"].(float64))

	createProviderReq(t, r, "Second Provider", "model-b")

	settings := getSettings(t, r)
	assert.Equal(t, firstID, settings.DefaultProviderID, "default provider should not change when a second provider is added")
}

// TestDeleteProvider_DefaultReassignsToRemainingProvider verifies that
// deleting the current default provider reassigns the default to another
// remaining provider instead of leaving the system without one.
func TestDeleteProvider_DefaultReassignsToRemainingProvider(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(database)

	createProviderReq(t, r, "First Provider", "model-a")
	second := createProviderReq(t, r, "Second Provider", "model-b")
	secondID := int32(second["id"].(float64))

	// Delete the default provider (the first one, ID 1).
	req := httptest.NewRequest(http.MethodDelete, "/providers/1", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	settings := getSettings(t, r)
	assert.Equal(t, secondID, settings.DefaultProviderID, "default provider should fall back to the remaining provider")
}

// TestDeleteProvider_LastProviderClearsDefault verifies that deleting the
// only remaining provider clears the default to 0 (unset) — the one
// legitimate situation where nothing can resolve to a model.
func TestDeleteProvider_LastProviderClearsDefault(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(database)

	createProviderReq(t, r, "Only Provider", "model-a")

	req := httptest.NewRequest(http.MethodDelete, "/providers/1", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	settings := getSettings(t, r)
	assert.Equal(t, int32(0), settings.DefaultProviderID)
}

// TestDeleteProvider_ResetsExplicitRoleAssignment verifies that deleting a
// provider a role was *explicitly* pointed at (via Role Model Configuration)
// resets that role to unset rather than leaving it dangling on a deleted ID.
func TestDeleteProvider_ResetsExplicitRoleAssignment(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(database)

	first := createProviderReq(t, r, "First Provider", "model-a")
	firstID := int32(first["id"].(float64))
	createProviderReq(t, r, "Second Provider", "model-b")

	// Explicitly point Coder at the first provider (simulating a manual
	// Role Model Configuration choice), distinct from the default provider.
	settings := getSettings(t, r)
	settings.RoleModels.CoderProviderID = firstID
	settings.RoleModels.CoderModel = "custom-model"
	saveSettings(t, r, settings)

	req := httptest.NewRequest(http.MethodDelete, "/providers/1", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	after := getSettings(t, r)
	assert.Equal(t, int32(0), after.RoleModels.CoderProviderID, "explicit role assignment to a deleted provider should reset to unset")
	assert.Empty(t, after.RoleModels.CoderModel)
}
