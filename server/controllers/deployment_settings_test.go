package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupDeploymentSettingsTest(t *testing.T) (*gorm.DB, *API, db.User, db.User) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.EnsureSchema(database))

	q := db.New(database)
	first, err := q.CreateUser(context.Background(), "admin@test.local")
	require.NoError(t, err)
	second, err := q.CreateUser(context.Background(), "member@test.local")
	require.NoError(t, err)
	return database, NewAPI(database, nil, nil), first, second
}

func requestAsUser(req *http.Request, user db.User) *http.Request {
	return req.WithContext(authctx.WithUser(req.Context(), user))
}

func TestRequireInstanceAdminUsesPersistedFlag(t *testing.T) {
	database, api, first, second := setupDeploymentSettingsTest(t)
	defer func() { sqlDB, _ := database.DB(); sqlDB.Close() }()
	require.True(t, first.IsAdmin)
	require.False(t, second.IsAdmin)
	// Authorization follows the persisted attribute, not the user's position in
	// registration order.
	require.NoError(t, database.Model(&db.User{}).Where("id = ?", first.ID).Update("is_admin", false).Error)
	require.NoError(t, database.Model(&db.User{}).Where("id = ?", second.ID).Update("is_admin", true).Error)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := api.RequireInstanceAdmin(next)

	adminReq := requestAsUser(httptest.NewRequest(http.MethodGet, "/deploy/status", nil), first)
	adminW := httptest.NewRecorder()
	h.ServeHTTP(adminW, adminReq)
	require.Equal(t, http.StatusForbidden, adminW.Code)

	memberReq := requestAsUser(httptest.NewRequest(http.MethodGet, "/deploy/status", nil), second)
	memberW := httptest.NewRecorder()
	h.ServeHTTP(memberW, memberReq)
	require.Equal(t, http.StatusOK, memberW.Code)
}

func TestDeploymentSettingsAreRedactedAndAdminCanSave(t *testing.T) {
	_, api, first, second := setupDeploymentSettingsTest(t)
	home := t.TempDir()
	t.Setenv("E2E_HEADCOUNT1_HOME", home)
	base := filepath.Join(home, ".headcount1")
	require.NoError(t, os.MkdirAll(base, 0o755))
	require.NoError(t, SaveSettings(Settings{
		BasePath: base, WorkspaceFolders: []string{"private"},
		GitRemoteURL: "https://example.com/headcount1.git",
		DeploySource: "releases", AutoDeploy: true,
	}))

	memberReq := requestAsUser(httptest.NewRequest(http.MethodGet, "/settings", nil), second)
	memberW := httptest.NewRecorder()
	api.GetSettings(memberW, memberReq)
	var redacted map[string]any
	require.NoError(t, json.Unmarshal(memberW.Body.Bytes(), &redacted))
	_, hasDeploySource := redacted["deploy_source"]
	_, hasAutoDeploy := redacted["auto_deploy"]
	require.False(t, hasDeploySource)
	require.False(t, hasAutoDeploy)
	require.Equal(t, "https://example.com/headcount1.git", redacted["git_remote_url"])

	adminReq := requestAsUser(httptest.NewRequest(http.MethodGet, "/settings", nil), first)
	adminW := httptest.NewRecorder()
	api.GetSettings(adminW, adminReq)
	var visible Settings
	require.NoError(t, json.Unmarshal(adminW.Body.Bytes(), &visible))
	require.Equal(t, "releases", visible.DeploySource)
	require.True(t, visible.AutoDeploy)

	body, err := json.Marshal(map[string]any{"deploy_source": "main", "auto_deploy": false})
	require.NoError(t, err)
	saveReq := requestAsUser(httptest.NewRequest(http.MethodPost, "/settings", bytes.NewReader(body)), first)
	saveW := httptest.NewRecorder()
	api.RequireInstanceAdmin(http.HandlerFunc(api.UpdateSettings)).ServeHTTP(saveW, saveReq)
	require.Equal(t, http.StatusOK, saveW.Code, saveW.Body.String())
	require.Equal(t, "main", LoadSettings().DeploySource)
	require.False(t, LoadSettings().AutoDeploy)

	deniedReq := requestAsUser(httptest.NewRequest(http.MethodPost, "/settings", bytes.NewReader(body)), second)
	deniedW := httptest.NewRecorder()
	api.RequireInstanceAdmin(http.HandlerFunc(api.UpdateSettings)).ServeHTTP(deniedW, deniedReq)
	require.Equal(t, http.StatusForbidden, deniedW.Code)
}

func TestAuthStateMarksFirstUserAsAdmin(t *testing.T) {
	_, api, first, second := setupDeploymentSettingsTest(t)

	firstState := api.authStateResponse(context.Background(), first)
	secondState := api.authStateResponse(context.Background(), second)
	require.Equal(t, true, firstState["is_admin"])
	require.Equal(t, false, secondState["is_admin"])
}
