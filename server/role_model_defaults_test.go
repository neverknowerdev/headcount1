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

// TestCreateProvider_FirstProviderDefaultsAllRoles verifies that creating the
// very first LLM provider in the system automatically assigns it to every AI
// role, so no role is ever left unset while a provider exists.
func TestCreateProvider_FirstProviderDefaultsAllRoles(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(database)

	created := createProviderReq(t, r, "First Provider", "model-a")
	providerID := int32(created["id"].(float64))

	settings := getSettings(t, r)
	rm := settings.RoleModels
	assert.Equal(t, providerID, rm.SmartPlannerProviderID)
	assert.Equal(t, providerID, rm.TechResearcherProviderID)
	assert.Equal(t, providerID, rm.WritingResearcherProviderID)
	assert.Equal(t, providerID, rm.DesignResearcherProviderID)
	assert.Equal(t, providerID, rm.CoderProviderID)
	assert.Equal(t, providerID, rm.TesterProviderID)
	// Model overrides left blank — resolves to the provider's own default.
	assert.Empty(t, rm.SmartPlannerModel)
	assert.Empty(t, rm.CoderModel)
}

// TestCreateProvider_SecondProviderDoesNotOverrideRoles verifies that adding
// an additional provider (when one already exists and roles are configured)
// leaves existing role assignments untouched.
func TestCreateProvider_SecondProviderDoesNotOverrideRoles(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(database)

	first := createProviderReq(t, r, "First Provider", "model-a")
	firstID := int32(first["id"].(float64))

	createProviderReq(t, r, "Second Provider", "model-b")

	settings := getSettings(t, r)
	assert.Equal(t, firstID, settings.RoleModels.SmartPlannerProviderID, "existing role assignment should not change when a second provider is added")
}

// TestDeleteProvider_ReassignsRolesToRemainingProvider verifies that deleting
// a provider currently assigned to roles reassigns them to another remaining
// provider instead of leaving them dangling.
func TestDeleteProvider_ReassignsRolesToRemainingProvider(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(database)

	first := createProviderReq(t, r, "First Provider", "model-a")
	firstID := int32(first["id"].(float64))
	second := createProviderReq(t, r, "Second Provider", "model-b")
	secondID := int32(second["id"].(float64))

	// Delete the provider all roles currently point to (the first one).
	req := httptest.NewRequest(http.MethodDelete, "/providers/1", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	settings := getSettings(t, r)
	rm := settings.RoleModels
	assert.NotEqual(t, firstID, rm.SmartPlannerProviderID)
	assert.Equal(t, secondID, rm.SmartPlannerProviderID, "role should fall back to the remaining provider")
	assert.Equal(t, secondID, rm.CoderProviderID)
	assert.Empty(t, rm.SmartPlannerModel, "model override should reset since it may not apply to the new provider")
}

// TestDeleteProvider_LastProviderClearsRoles verifies that deleting the only
// remaining provider clears role assignments to 0 (unset) — the one
// legitimate situation where a role has no resolvable model.
func TestDeleteProvider_LastProviderClearsRoles(t *testing.T) {
	database := setupProvidersTestDB(t)
	r := setupProvidersRouter(database)

	createProviderReq(t, r, "Only Provider", "model-a")

	req := httptest.NewRequest(http.MethodDelete, "/providers/1", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	settings := getSettings(t, r)
	assert.Equal(t, int32(0), settings.RoleModels.SmartPlannerProviderID)
	assert.Equal(t, int32(0), settings.RoleModels.TesterProviderID)
}
