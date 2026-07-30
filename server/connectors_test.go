package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
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

// mockVercel captures every request the sync layer sends to "Vercel".
type mockVercel struct {
	mu       sync.Mutex
	requests []string
	bodies   [][]byte
	srv      *httptest.Server
}

func newMockVercel(t *testing.T) *mockVercel {
	m := &mockVercel{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.requests = append(m.requests, r.Method+" "+r.URL.RequestURI())
		m.bodies = append(m.bodies, body)
		m.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			w.Write([]byte(`{"envs":[{"id":"e1","key":"API_KEY","target":["production"]}],"projects":[{"id":"prj_1","name":"web"}]}`))
		default:
			w.Write([]byte(`{"created":[],"failed":[]}`))
		}
	}))
	t.Cleanup(m.srv.Close)
	t.Setenv("VERCEL_API_URL", m.srv.URL)
	return m
}

func (m *mockVercel) snapshot() ([]string, [][]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.requests...), append([][]byte(nil), m.bodies...)
}

func setupConnectorsTest(t *testing.T) (chi.Router, *gorm.DB, db.Project, int32) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(
		&db.User{}, &db.Team{}, &db.TeamMember{}, &db.Company{}, &db.Project{},
		&db.Environment{}, &db.EnvironmentSecret{}, &db.EnvironmentConnector{},
	))
	q := db.New(database)
	api := endpoints.NewAPI(database, nil, nil)

	r := chi.NewRouter()
	r.Route("/projects/{id}", func(r chi.Router) {
		r.Get("/environments", api.ListProjectEnvironments)
		r.Post("/environments", api.CreateProjectEnvironment)
	})
	r.Route("/environments/{envID}", func(r chi.Router) {
		r.Put("/secrets", api.UpsertEnvironmentSecret)
		r.Delete("/secrets/{name}", api.DeleteEnvironmentSecret)
		r.Post("/connectors", api.CreateEnvironmentConnector)
		r.Post("/sync", api.SyncEnvironment)
	})
	r.Delete("/connectors/{connectorID}", api.DeleteEnvironmentConnector)
	router := withTestUser(t, database, r)

	uid := testSeedUserID(t, q)
	company := db.Company{Name: "SyncCo", ShortName: "SYN", UserID: &uid}
	require.NoError(t, database.Create(&company).Error)
	project := db.Project{CompanyID: company.ID, Name: "Web"}
	require.NoError(t, database.Create(&project).Error)

	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(uid, dek, time.Minute)
	t.Cleanup(func() { secrets.Default().LockUser(uid) })
	return router, database, project, uid
}

func TestConnectorSyncPushesEntriesToTarget(t *testing.T) {
	mock := newMockVercel(t)
	router, database, project, _ := setupConnectorsTest(t)

	// Create a project environment.
	b, _ := json.Marshal(map[string]string{"name": "production"})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/projects/%d/environments", project.ID), bytes.NewReader(b))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	var env struct{ ID int32 }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))

	// Add a secret and a variable BEFORE connecting.
	for _, payload := range []map[string]string{
		{"name": "API_KEY", "value": "prod-api-secret-9876", "kind": "secret"},
		{"name": "NODE_ENV", "value": "production", "kind": "variable"},
	} {
		b, _ := json.Marshal(payload)
		req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/environments/%d/secrets", env.ID), bytes.NewReader(b))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	}

	// Connect to "Vercel" — the token must never come back in the response.
	b, _ = json.Marshal(map[string]string{
		"provider":           "vercel",
		"token":              "vercel-token-verysecret-123",
		"target_project":     "prj_1",
		"target_environment": "production",
	})
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/environments/%d/connectors", env.ID), bytes.NewReader(b))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	assert.NotContains(t, w.Body.String(), "vercel-token-verysecret")
	assert.Contains(t, w.Body.String(), `"has_token":true`)

	// The token is sealed at rest.
	var rawTok string
	require.NoError(t, database.Raw("SELECT api_token FROM environment_connectors").Scan(&rawTok).Error)
	require.True(t, secrets.IsSealed(rawTok))

	// Deterministic push via the sync endpoint.
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/environments/%d/sync", env.ID), nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var connectors []db.EnvironmentConnector
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &connectors))
	require.Len(t, connectors, 1)
	assert.Equal(t, "ok", connectors[0].LastSyncStatus)
	assert.NotNil(t, connectors[0].LastSyncAt)

	// The mock target received the upsert with both entries, correct types.
	requests, bodies := mock.snapshot()
	var pushBody []byte
	for i, r := range requests {
		if r == "POST /v10/projects/prj_1/env?upsert=true" {
			pushBody = bodies[i]
		}
	}
	require.NotNil(t, pushBody, "expected an upsert push, got: %v", requests)
	var items []map[string]interface{}
	require.NoError(t, json.Unmarshal(pushBody, &items))
	require.Len(t, items, 2)
	byKey := map[string]map[string]interface{}{}
	for _, it := range items {
		byKey[it["key"].(string)] = it
	}
	assert.Equal(t, "sensitive", byKey["API_KEY"]["type"])
	assert.Equal(t, "prod-api-secret-9876", byKey["API_KEY"]["value"], "the target receives the real plaintext")
	assert.Equal(t, "plain", byKey["NODE_ENV"]["type"])

	// Deleting an entry also deletes it on the target (async — poll).
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/environments/%d/secrets/API_KEY", env.ID), nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Eventually(t, func() bool {
		requests, _ := mock.snapshot()
		for _, r := range requests {
			if r == "DELETE /v9/projects/prj_1/env/e1" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "target delete should be issued")
}

func TestConnectorLifecycleAndTenantIsolation(t *testing.T) {
	newMockVercel(t)
	router, database, project, _ := setupConnectorsTest(t)

	b, _ := json.Marshal(map[string]string{"name": "preview"})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/projects/%d/environments", project.ID), bytes.NewReader(b))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	var env struct{ ID int32 }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))

	// Missing target fields are rejected.
	b, _ = json.Marshal(map[string]string{"provider": "vercel", "token": "tok-1234567890"})
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/environments/%d/connectors", env.ID), bytes.NewReader(b))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Create + delete round-trip.
	b, _ = json.Marshal(map[string]string{
		"provider": "vercel", "token": "tok-1234567890",
		"target_project": "prj_1", "target_environment": "preview",
	})
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/environments/%d/connectors", env.ID), bytes.NewReader(b))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	var conn db.EnvironmentConnector
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &conn))

	// A foreign user's environment/connector is 404 — simulate by pointing
	// the environment at another company.
	other := db.Company{Name: "Other", ShortName: "OTH"}
	require.NoError(t, database.Create(&other).Error)
	require.NoError(t, database.Model(&db.Environment{}).Where("id = ?", env.ID).Update("company_id", other.ID).Error)
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/connectors/%d", conn.ID), nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	// Restore ownership → delete succeeds.
	require.NoError(t, database.Model(&db.Environment{}).Where("id = ?", env.ID).Update("company_id", project.CompanyID).Error)
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/connectors/%d", conn.ID), nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
}
