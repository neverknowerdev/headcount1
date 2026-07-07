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

func setupModelGroupsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.LLMProvider{}, &db.ModelGroup{}, &db.ModelGroupMember{}, &db.ModelRequestStat{}))
	return database
}

func setupModelGroupsRouter(database *gorm.DB) chi.Router {
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Get("/model-groups", api.ListModelGroups)
	r.Post("/model-groups", api.CreateModelGroup)
	r.Put("/model-groups/{id}", api.UpdateModelGroup)
	r.Delete("/model-groups/{id}", api.DeleteModelGroup)
	r.Get("/model-groups/{id}/stats", api.GetModelGroupStats)
	return r
}

func TestModelGroup_CannotDeleteBuiltin(t *testing.T) {
	database := setupModelGroupsTestDB(t)
	r := setupModelGroupsRouter(database)
	q := db.New(database)

	require.NoError(t, database.Create(&db.LLMProvider{Name: "OpenRouter Free Models", ProviderName: db.ProviderVendorOpenRouter, Builtin: true}).Error)
	require.NoError(t, q.EnsureDefaultModelGroups(context.Background()))

	group, err := q.GetModelGroupByKey(context.Background(), db.DefaultUtilityGroupSlug)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/model-groups/%d", group.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Still there afterwards.
	var stillThere db.ModelGroup
	assert.NoError(t, database.First(&stillThere, group.ID).Error)
}

func TestModelGroup_CanDeleteCustom(t *testing.T) {
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
	assert.False(t, created.Builtin)

	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/model-groups/%d", created.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
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
