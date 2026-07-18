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

func setupMCPTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	err = database.AutoMigrate(
		&db.User{},
		&db.Company{},
		&db.Agent{},
		&db.MCPServer{},
		&db.AgentMCPServer{},
		&db.MCPAccount{},
		&db.AgentMCPAccount{},
	)
	require.NoError(t, err)
	return database
}

func setupMCPRouter(t *testing.T, database *gorm.DB) chi.Router {
	api := endpoints.NewAPI(database, nil, nil)
	r := chi.NewRouter()
	r.Get("/mcp-servers", api.ListMCPServers)
	r.Post("/mcp-servers", api.CreateMCPServer)
	r.Route("/mcp-servers/{id}", func(r chi.Router) {
		r.Use(api.LoadMCPServer)
		r.Get("/", api.GetMCPServer)
		r.Put("/", api.UpdateMCPServer)
		r.Delete("/", api.DeleteMCPServer)
	})
	r.Route("/agents/{id}/mcp-servers", func(r chi.Router) {
		r.Use(api.LoadAgent)
		r.Get("/", api.GetAgentMCPServers)
		r.Put("/", api.SetAgentMCPServers)
	})
	return withTestUser(t, database, r)
}

func seedMCPTestCompany(t *testing.T, database *gorm.DB) db.Company {
	t.Helper()
	q := db.New(database)
	c, err := q.CreateCompany(context.Background(), "Test Co")
	require.NoError(t, err)
	// Owned by the fixture user so authorize* checks pass.
	uid := testSeedUserID(t, q)
	require.NoError(t, database.Model(&c).Update("user_id", uid).Error)
	return c
}

func TestMCPServerCRUD(t *testing.T) {
	database := setupMCPTestDB(t)
	r := setupMCPRouter(t, database)

	// Create
	payload, _ := json.Marshal(db.MCPServer{
		Name:        "github",
		DisplayName: "GitHub MCP",
		Transport:   "stdio",
		Command:     "/usr/local/bin/github-mcp",
		Enabled:     true,
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp-servers", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)

	var created db.MCPServer
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.Equal(t, "github", created.Name)
	assert.True(t, created.Enabled)
	assert.False(t, created.Builtin) // UI-created is never builtin

	// List
	req = httptest.NewRequest(http.MethodGet, "/mcp-servers", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var list []db.MCPServer
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.Len(t, list, 1)

	// Get
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mcp-servers/%d", created.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Update
	created.DisplayName = "GitHub (Updated)"
	updatePayload, _ := json.Marshal(created)
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/mcp-servers/%d", created.ID), bytes.NewReader(updatePayload))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var updated db.MCPServer
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.Equal(t, "GitHub (Updated)", updated.DisplayName)

	// Delete
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/mcp-servers/%d", created.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Confirm deleted
	req = httptest.NewRequest(http.MethodGet, "/mcp-servers", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.Empty(t, list)
}

func TestMCPServer_CannotDeleteBuiltin(t *testing.T) {
	database := setupMCPTestDB(t)
	r := setupMCPRouter(t, database)

	// Create the built-in servers and pick one to test deletion protection.
	q := db.New(database)
	require.NoError(t, q.EnsureBuiltinMCPServers(context.Background()))
	var builtin db.MCPServer
	require.NoError(t, database.Where("builtin = ?", true).First(&builtin).Error)

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/mcp-servers/%d", builtin.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestAgentMCPAssignments(t *testing.T) {
	database := setupMCPTestDB(t)
	r := setupMCPRouter(t, database)
	company := seedMCPTestCompany(t, database)

	// Create MCP server (global, no company).
	q := db.New(database)
	srv, err := q.CreateMCPServer(context.Background(), db.MCPServer{
		Name: "test", Transport: "http", URL: "http://localhost", Enabled: true,
	})
	require.NoError(t, err)

	// Create agent.
	agent, err := q.CreateAgent(context.Background(), db.Agent{
		CompanyID: company.ID, Name: "TestAgent", SystemPrompt: "...", Mode: "primary",
	})
	require.NoError(t, err)

	// Assign MCP server to agent.
	assignments := []db.AgentMCPServer{{MCPServerID: srv.ID, Enabled: true}}
	payload, _ := json.Marshal(assignments)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/agents/%d/mcp-servers", agent.ID), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// List assignments.
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/agents/%d/mcp-servers", agent.ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var result []db.AgentMCPServer
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	assert.Len(t, result, 1)
	assert.Equal(t, srv.ID, result[0].MCPServerID)
	assert.True(t, result[0].Enabled)
}

func TestMCPServer_CreateRequiresNameAndTransport(t *testing.T) {
	database := setupMCPTestDB(t)
	r := setupMCPRouter(t, database)

	payload, _ := json.Marshal(map[string]string{"name": "incomplete"})
	req := httptest.NewRequest(http.MethodPost, "/mcp-servers", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
