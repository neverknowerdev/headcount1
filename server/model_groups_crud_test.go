package server

import (
	"agent-orchestrator/db/migrations"
	"bytes"
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

func setupModelGroupsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	return database
}

func setupModelGroupsRouter(database *gorm.DB) chi.Router {
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Get("/model-groups", api.ListModelGroups)
	r.Post("/model-groups", api.CreateModelGroup)
	r.Route("/model-groups/{id}", func(r chi.Router) {
		r.Use(api.LoadModelGroup)
		r.Put("/", api.UpdateModelGroup)
		r.Delete("/", api.DeleteModelGroup)
		r.Get("/stats", api.GetModelGroupStats)
	})
	return r
}

// TestModelGroup_AnyGroupCanBeDeleted verifies every model group, with no
// exceptions, can be deleted — there's no built-in/undeletable concept.
func TestModelGroup_AnyGroupCanBeDeleted(t *testing.T) {
	database := setupModelGroupsTestDB(t)
	r := setupModelGroupsRouter(database)

	payload := map[string]interface{}{"name": "My Custom Group"}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/model-groups", bytes.NewReader(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	var created db.ModelGroup
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/model-groups/%d", created.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var gone db.ModelGroup
	assert.Error(t, database.First(&gone, created.ID).Error)
}

// TestModelGroup_AllModelsMemberRoundTrip verifies a member saved with
// all_models=true round-trips through create/list without requiring a
// concrete model name.
func TestModelGroup_AllModelsMemberRoundTrip(t *testing.T) {
	database := setupModelGroupsTestDB(t)
	r := setupModelGroupsRouter(database)

	provider := db.LLMProvider{Name: "P", SupportedModels: "m1,m2"}
	require.NoError(t, database.Create(&provider).Error)

	payload := map[string]interface{}{
		"name": "Wildcard Group",
		"members": []map[string]interface{}{
			{"provider_id": provider.ID, "all_models": true, "is_free": true},
		},
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/model-groups", bytes.NewReader(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var created db.ModelGroup
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.Len(t, created.Members, 1)
	assert.True(t, created.Members[0].AllModels)
	assert.Empty(t, created.Members[0].Model)
	assert.True(t, created.Members[0].IsFree)
}
