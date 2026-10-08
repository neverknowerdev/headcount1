package server

import (
	"agent-orchestrator/db/migrations"
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
	"agent-orchestrator/engine/enginetest"
	endpoints "agent-orchestrator/server/controllers"
)

func setupDefaultModelSettingsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	return database
}

func setupDefaultModelSettingsRouter(t *testing.T, database *gorm.DB) chi.Router {
	router, _ := setupDefaultModelSettingsRouterWithEngine(t, database)
	return router
}

func setupDefaultModelSettingsRouterWithEngine(t *testing.T, database *gorm.DB) (chi.Router, *enginetest.Recorder) {
	recorder := &enginetest.Recorder{}
	api := endpoints.NewAPI(database, recorder, nil)
	r := chi.NewRouter()
	r.Get("/default-model-settings", api.ListDefaultModelSettings)
	r.Put("/default-model-settings/{purpose}", api.UpdateDefaultModelSetting)
	return withTestUser(t, database, r), recorder
}

func TestDefaultModelSettings_ListAndUpdate(t *testing.T) {
	database := setupDefaultModelSettingsTestDB(t)
	r, recorder := setupDefaultModelSettingsRouterWithEngine(t, database)
	q := db.New(database)
	uid := testSeedUserID(t, q)
	require.NoError(t, q.EnsureDefaultModelSettingsForUser(context.Background(), uid))

	// List shows every model slot, initially unconfigured.
	req := httptest.NewRequest(http.MethodGet, "/default-model-settings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var list []db.DefaultModelSetting
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list, 4)

	// Point commit_messages at a fixed provider+model.
	provider := db.LLMProvider{Name: "P", DefaultModel: "p-default", UserID: &uid}
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

	// Point the cheap tier at a model group instead.
	group := db.ModelGroup{Name: "G", Slug: "g", UserID: &uid}
	require.NoError(t, database.Create(&group).Error)
	payload = map[string]interface{}{"model_group_id": group.ID}
	b, _ = json.Marshal(payload)
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/default-model-settings/%s", db.PurposeCheap), bytes.NewReader(b))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	require.NotNil(t, updated.ModelGroupID)
	assert.Equal(t, group.ID, *updated.ModelGroupID)
	assert.Nil(t, updated.ProviderID)

	// Each change tells the engine, so a task waiting for a model goes on.
	assert.Equal(t, []string{"credentials-changed", "credentials-changed"}, recorder.Calls())
}

func TestDefaultModelSettings_UpdateUnknownPurpose(t *testing.T) {
	database := setupDefaultModelSettingsTestDB(t)
	r := setupDefaultModelSettingsRouter(t, database)

	req := httptest.NewRequest(http.MethodPut, "/default-model-settings/not-a-real-purpose", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// The classifier slot takes a classifier and nothing else, and a classifier
// cannot stand in for a language model anywhere.
func TestDefaultModelSettings_ClassifierSlotTakesOnlyAClassifier(t *testing.T) {
	database := setupDefaultModelSettingsTestDB(t)
	r := setupDefaultModelSettingsRouter(t, database)
	q := db.New(database)
	uid := testSeedUserID(t, q)
	require.NoError(t, q.EnsureDefaultModelSettingsForUser(context.Background(), uid))
	chat := db.LLMProvider{Name: "Chat", ProviderType: "openai", DefaultModel: "m", UserID: &uid}
	require.NoError(t, database.Create(&chat).Error)
	jev := db.LLMProvider{Name: "TypeSafe", ProviderType: "typesafe", DefaultModel: "jev-latest", UserID: &uid}
	require.NoError(t, database.Create(&jev).Error)
	group := db.ModelGroup{Name: "G", Slug: "g", UserID: &uid}
	require.NoError(t, database.Create(&group).Error)

	put := func(purpose string, payload map[string]interface{}) int {
		b, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/default-model-settings/"+purpose, bytes.NewReader(b)))
		return w.Code
	}
	assert.Equal(t, http.StatusBadRequest, put(db.PurposeClassifier, map[string]interface{}{"provider_id": chat.ID, "model": "m"}))
	assert.Equal(t, http.StatusBadRequest, put(db.PurposeClassifier, map[string]interface{}{"model_group_id": group.ID}))
	assert.Equal(t, http.StatusOK, put(db.PurposeClassifier, map[string]interface{}{"provider_id": jev.ID, "model": "jev-latest"}))
	assert.Equal(t, http.StatusBadRequest, put(db.PurposeSmart, map[string]interface{}{"provider_id": jev.ID, "model": "jev-latest"}))
	assert.Equal(t, http.StatusBadRequest, put(db.PurposeCheap, map[string]interface{}{"provider_id": jev.ID, "model": "jev-latest"}))
	// Clearing the slot is always allowed.
	assert.Equal(t, http.StatusOK, put(db.PurposeClassifier, map[string]interface{}{}))
}
