package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/pkg/agentdefaults"
	endpoints "agent-orchestrator/server/controllers"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAgentsRouter(t *testing.T, database *gorm.DB) chi.Router {
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Get("/agents", api.ListAgents)
	r.Route("/agents/{id}", func(r chi.Router) {
		r.Use(api.LoadAgent)
		r.Put("/", api.UpdateAgent)
		r.Delete("/", api.DeleteAgent)
	})
	return withTestUser(t, database, r)
}

func TestBuiltinAgentIsAlwaysEnabledAllowsPromptUpdatesAndCannotBeDeleted(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	uid := testSeedUserID(t, db.New(database))
	company := db.Company{Name: "Acme", UserID: &uid}
	require.NoError(t, database.Create(&company).Error)
	q := db.New(database)
	require.NoError(t, q.EnsureBuiltinAgentsForCompany(context.Background(), company.ID, agentdefaults.Rows(company.ID)))
	var builtin db.Agent
	require.NoError(t, database.Where("company_id = ? AND role_key = ?", company.ID, "CEO").First(&builtin).Error)

	r := setupAgentsRouter(t, database)
	deleteReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/agents/%d", builtin.ID), nil)
	deleteW := httptest.NewRecorder()
	r.ServeHTTP(deleteW, deleteReq)
	assert.Equal(t, http.StatusForbidden, deleteW.Code)

	payload, _ := json.Marshal(map[string]any{
		"enabled":       false,
		"name":          "Should Not Change",
		"system_prompt": "You are the CEO agent. We sell boats.",
	})
	updateReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/agents/%d", builtin.ID), bytes.NewReader(payload))
	updateW := httptest.NewRecorder()
	r.ServeHTTP(updateW, updateReq)
	require.Equal(t, http.StatusOK, updateW.Code)
	var updated db.Agent
	require.NoError(t, json.Unmarshal(updateW.Body.Bytes(), &updated))
	assert.True(t, updated.Enabled)
	assert.Equal(t, "CEO", updated.Name)
	assert.Equal(t, "You are the CEO agent. We sell boats.", updated.SystemPrompt)
	assert.True(t, updated.Builtin)
	// An agent is a role; the API no longer speaks of models, tools or MCPs.
	var wire map[string]any
	require.NoError(t, json.Unmarshal(updateW.Body.Bytes(), &wire))
	for _, gone := range []string{"model", "provider_id", "model_group_id", "permissions", "allowed_mcps", "can_use_workers", "chat_type"} {
		assert.NotContains(t, wire, gone)
	}

	custom, err := q.CreateAgent(context.Background(), db.Agent{
		CompanyID: company.ID, Name: "Custom researcher", SystemPrompt: "Research the task.",
	})
	require.NoError(t, err)
	customDeleteReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/agents/%d", custom.ID), nil)
	customDeleteW := httptest.NewRecorder()
	r.ServeHTTP(customDeleteW, customDeleteReq)
	assert.Equal(t, http.StatusOK, customDeleteW.Code)
	_, err = q.GetAgent(context.Background(), custom.ID)
	assert.Error(t, err)
}

func TestCreateCompanySeedsBuiltinAgentsAndModelTiers(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	uid := testSeedUserID(t, db.New(database))
	provider := db.LLMProvider{UserID: &uid, Name: "Test", BaseUrl: "https://example.test", DefaultModel: "test-model"}
	require.NoError(t, database.Create(&provider).Error)

	api := endpoints.NewAPI(database, nil, nil)
	baseRouter := chi.NewRouter()
	baseRouter.Post("/companies", api.CreateCompany)
	router := withTestUser(t, database, baseRouter)
	payload, _ := json.Marshal(map[string]any{
		"name": "Acme", "short_name": "acme", "provider_id": provider.ID, "model": "test-model",
	})
	req := httptest.NewRequest(http.MethodPost, "/companies", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	var company db.Company
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &company))
	var count int64
	require.NoError(t, database.Model(&db.Agent{}).Where("company_id = ? AND builtin = ? AND enabled = ?", company.ID, true, true).Count(&count).Error)
	assert.Equal(t, int64(13), count)

	// The model picked while creating a first company becomes the user's smart
	// and cheap default, so its first task can run.
	q := db.New(database)
	for _, purpose := range []string{db.PurposeSmart, db.PurposeCheap} {
		setting, err := q.GetDefaultModelSetting(context.Background(), uid, purpose)
		require.NoError(t, err, purpose)
		require.NotNil(t, setting.ProviderID, purpose)
		assert.Equal(t, provider.ID, *setting.ProviderID, purpose)
		assert.Equal(t, "test-model", setting.Model, purpose)
	}

	// A tier the user has since configured is not overwritten by another company.
	_, err = q.UpdateDefaultModelSetting(context.Background(), uid, db.PurposeCheap, &provider.ID, "tiny-model", nil)
	require.NoError(t, err)
	payload, _ = json.Marshal(map[string]any{"name": "Second", "short_name": "second", "provider_id": provider.ID, "model": "test-model"})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/companies", bytes.NewReader(payload)))
	require.Equal(t, http.StatusCreated, w.Code)
	cheap, err := q.GetDefaultModelSetting(context.Background(), uid, db.PurposeCheap)
	require.NoError(t, err)
	assert.Equal(t, "tiny-model", cheap.Model)
}
