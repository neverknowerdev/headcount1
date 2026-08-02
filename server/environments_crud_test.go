package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"
	endpoints "agent-orchestrator/server/controllers"
)

func setupEnvironmentsTest(t *testing.T) (*gorm.DB, chi.Router, db.Company, int32) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(
		&db.User{}, &db.Team{}, &db.TeamMember{}, &db.Company{}, &db.Project{},
		&db.Environment{}, &db.EnvironmentSecret{}, &db.EnvironmentConnector{}, &db.Task{},
		&db.LLMProvider{}, &db.MCPServer{}, &db.MCPAccount{}, &db.UserGitCredential{},
	))
	q := db.New(database)
	api := endpoints.NewAPI(database, nil, nil)

	r := chi.NewRouter()
	r.Route("/companies/{id}", func(r chi.Router) {
		r.Get("/environments", api.ListEnvironments)
		r.Post("/environments", api.CreateEnvironment)
	})
	r.Route("/environments/{envID}", func(r chi.Router) {
		r.Put("/", api.UpdateEnvironment)
		r.Delete("/", api.DeleteEnvironment)
		r.Put("/secrets", api.UpsertEnvironmentSecret)
		r.Delete("/secrets/{name}", api.DeleteEnvironmentSecret)
	})
	router := withTestUser(t, database, r)

	uid := testSeedUserID(t, q)
	company := db.Company{Name: "EnvCo", ShortName: "ENV", UserID: &uid}
	require.NoError(t, database.Create(&company).Error)

	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(uid, dek, time.Minute)
	t.Cleanup(func() { secrets.Default().LockUser(uid) })
	return database, router, company, uid
}

type envResp struct {
	db.Environment
	Secrets       []db.EnvironmentSecret `json:"secrets"`
	SystemSecrets []struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		HasValue bool   `json:"has_value"`
	} `json:"system_secrets"`
}

func TestEnvironmentsCRUDAndSecrets(t *testing.T) {
	database, r, company, uid := setupEnvironmentsTest(t)

	// Listing seeds the single platform environment.
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/companies/%d/environments", company.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var envs []envResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envs))
	require.Len(t, envs, 1)
	defaultEnv := envs[0]
	require.True(t, defaultEnv.IsDefault)
	require.Equal(t, "headcount1 cloud", defaultEnv.Name)

	// Create a user-defined environment.
	b, _ := json.Marshal(map[string]string{"name": "staging"})
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/companies/%d/environments", company.ID), bytes.NewReader(b))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	var staging envResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &staging))

	// Add a secret; response carries name + has_value, never the value.
	b, _ = json.Marshal(map[string]string{"name": "API_KEY", "value": "super-secret-env-value"})
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/environments/%d/secrets", staging.ID), bytes.NewReader(b))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "super-secret-env-value")
	assert.Contains(t, w.Body.String(), `"has_value":true`)

	// Stored sealed in the DB.
	var raw string
	require.NoError(t, database.Raw("SELECT value FROM environment_secrets WHERE environment_id = ? AND name = ?", staging.ID, "API_KEY").Scan(&raw).Error)
	require.True(t, secrets.IsSealed(raw))

	// Invalid env var name is rejected.
	b, _ = json.Marshal(map[string]string{"name": "BAD NAME", "value": "v"})
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/environments/%d/secrets", staging.ID), bytes.NewReader(b))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Locked vault → 409.
	secrets.Default().LockUser(uid)
	b, _ = json.Marshal(map[string]string{"name": "OTHER", "value": "v"})
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/environments/%d/secrets", staging.ID), bytes.NewReader(b))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusConflict, w.Code)
	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(uid, dek, time.Minute)

	// Builtins are protected.
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/environments/%d", defaultEnv.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	b, _ = json.Marshal(map[string]string{"name": "renamed"})
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/environments/%d", defaultEnv.ID), bytes.NewReader(b))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Delete secret, then the environment.
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/environments/%d/secrets/API_KEY", staging.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/environments/%d", staging.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}

// TestEnvironmentsTenantIsolation: a company owned by another user is 404 for
// the caller, and so are its environments.
func TestEnvironmentsTenantIsolation(t *testing.T) {
	database, r, _, _ := setupEnvironmentsTest(t)
	q := db.New(database)

	other, err := q.CreateUser(context.Background(), "other@corp.io")
	require.NoError(t, err)
	foreign := db.Company{Name: "Foreign", ShortName: "FOR", UserID: &other.ID}
	require.NoError(t, database.Create(&foreign).Error)
	require.NoError(t, q.EnsureDefaultEnvironments(context.Background(), foreign.ID))
	foreignEnvs, err := q.ListEnvironments(context.Background(), foreign.ID)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/companies/%d/environments", foreign.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	b, _ := json.Marshal(map[string]string{"name": "X", "value": "v"})
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/environments/%d/secrets", foreignEnvs[0].ID), bytes.NewReader(b))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}
