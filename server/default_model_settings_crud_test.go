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

func setupDefaultModelSettingsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.LLMProvider{}, &db.ModelGroup{}, &db.ModelGroupMember{}, &db.DefaultModelSetting{}))
	return database
}

func setupDefaultModelSettingsRouter(database *gorm.DB) chi.Router {
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Get("/default-model-settings", api.ListDefaultModelSettings)
	r.Put("/default-model-settings/{purpose}", api.UpdateDefaultModelSetting)
	return r
}

func TestDefaultModelSettings_ListAndUpdate(t *testing.T) {
	database := setupDefaultModelSettingsTestDB(t)
	r := setupDefaultModelSettingsRouter(database)
	q := db.New(database)
	require.NoError(t, q.EnsureDefaultModelSettings(context.Background()))

	// List shows every known purpose, initially unconfigured (no Utility group in this DB).
	req := httptest.NewRequest(http.MethodGet, "/default-model-settings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var list []db.DefaultModelSetting
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list, 3)

	// Point commit_messages at a fixed provider+model.
	provider := db.LLMProvider{Name: "P", DefaultModel: "p-default"}
	require.NoError(t, database.Create(&provider).Error)
	payload := map[string]interface{}{"provider_id": provider.ID, "model": "my-model"}
	b, _ := json.Marshal(payload)
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/default-model-settings/%s", db.PurposeCommitMessages), bytes.NewReader(b))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var updated db.DefaultModelSetting
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	require.NotNil(t, updated.ProviderID)
	assert.Equal(t, provider.ID, *updated.ProviderID)
	assert.Equal(t, "my-model", updated.Model)
	assert.Nil(t, updated.ModelGroupID)

	// Point ask_artifact at a model group instead.
	group := db.ModelGroup{Name: "G", Slug: "g"}
	require.NoError(t, database.Create(&group).Error)
	payload = map[string]interface{}{"model_group_id": group.ID}
	b, _ = json.Marshal(payload)
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/default-model-settings/%s", db.PurposeAskArtifact), bytes.NewReader(b))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	require.NotNil(t, updated.ModelGroupID)
	assert.Equal(t, group.ID, *updated.ModelGroupID)
	assert.Nil(t, updated.ProviderID)
}

func TestDefaultModelSettings_UpdateUnknownPurpose(t *testing.T) {
	database := setupDefaultModelSettingsTestDB(t)
	r := setupDefaultModelSettingsRouter(database)

	req := httptest.NewRequest(http.MethodPut, "/default-model-settings/not-a-real-purpose", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
