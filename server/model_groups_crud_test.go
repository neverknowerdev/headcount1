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

// A group routes between language models or between System One models, never
// both: a model of the other kind is refused, and so is a change of kind.
func TestModelGroup_HoldsModelsOfOneKind(t *testing.T) {
	database := setupModelGroupsTestDB(t)
	r := setupModelGroupsRouter(database)
	zen := db.LLMProvider{Name: "Zen", SupportedModels: "big-pickle,jev-1.13,jev-1.13-free"}
	require.NoError(t, database.Create(&zen).Error)
	chat := db.LLMProvider{Name: "Chat only", SupportedModels: "m1"}
	require.NoError(t, database.Create(&chat).Error)

	send := func(method, path string, payload map[string]interface{}) (int, db.ModelGroup, string) {
		b, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(b)))
		var group db.ModelGroup
		_ = json.Unmarshal(w.Body.Bytes(), &group)
		return w.Code, group, w.Body.String()
	}
	member := func(provider db.LLMProvider, model string) map[string]interface{} {
		return map[string]interface{}{"provider_id": provider.ID, "model": model}
	}

	// The kind is taken from the models named when none is given.
	code, classifiers, body := send(http.MethodPost, "/model-groups", map[string]interface{}{
		"name": "Classifiers", "members": []map[string]interface{}{member(zen, "jev-1.13-free"), member(zen, "jev-1.13")},
	})
	require.Equal(t, http.StatusCreated, code, body)
	assert.Equal(t, db.ModelKindSystemOne, classifiers.Kind)
	require.Len(t, classifiers.Members, 2)

	code, writers, body := send(http.MethodPost, "/model-groups", map[string]interface{}{
		"name": "Writers", "members": []map[string]interface{}{member(zen, "big-pickle")},
	})
	require.Equal(t, http.StatusCreated, code, body)
	assert.Equal(t, db.ModelKindLLM, writers.Kind)

	// A mixed group is refused, and nothing of it is stored.
	code, _, body = send(http.MethodPost, "/model-groups", map[string]interface{}{
		"name": "Mixed", "members": []map[string]interface{}{member(zen, "big-pickle"), member(zen, "jev-1.13")},
	})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "jev-1.13 is a System One model and cannot be in a group of language models")
	var count int64
	require.NoError(t, database.Model(&db.ModelGroup{}).Where("name = ?", "Mixed").Count(&count).Error)
	assert.Zero(t, count)

	code, _, body = send(http.MethodPut, fmt.Sprintf("/model-groups/%d/", classifiers.ID), map[string]interface{}{
		"name": "Classifiers", "members": []map[string]interface{}{member(zen, "big-pickle")},
	})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "big-pickle is a language model and cannot be in a group of System One models")
	code, _, body = send(http.MethodPut, fmt.Sprintf("/model-groups/%d/", classifiers.ID), map[string]interface{}{"name": "Classifiers", "kind": "llm"})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "kind cannot be changed")

	// "All models" of a provider means all of the group's kind, so a provider
	// with none of that kind has nothing to add.
	code, all, body := send(http.MethodPost, "/model-groups", map[string]interface{}{
		"name": "Every classifier", "kind": "system_one", "members": []map[string]interface{}{{"provider_id": zen.ID, "all_models": true}},
	})
	require.Equal(t, http.StatusCreated, code, body)
	assert.Equal(t, db.ModelKindSystemOne, all.Kind)
	code, _, body = send(http.MethodPost, "/model-groups", map[string]interface{}{
		"name": "Nothing to route", "kind": "system_one", "members": []map[string]interface{}{{"provider_id": chat.ID, "all_models": true}},
	})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "Chat only has no System One models")
}
