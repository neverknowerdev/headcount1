package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpdateSettingsPreservesOmittedFields guards against a partial POST
// silently resetting settings it didn't mention. This matters most for
// auto_deploy, which defaults to TRUE: decoding into a zero value would turn
// "field omitted" into "deployments disabled".
func TestUpdateSettingsPreservesOmittedFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("E2E_HEADCOUNT1_HOME", home)
	base := filepath.Join(home, ".headcount1")
	require.NoError(t, os.MkdirAll(base, 0755))

	// Seed a settings file with values a client might not echo back.
	require.NoError(t, SaveSettings(Settings{
		BasePath:         base,
		WorkspaceFolders: []string{"alpha", "beta"},
		DeploySource:     "main",
		AutoDeploy:       true,
	}))

	api := &API{}
	body, _ := json.Marshal(map[string]any{"base_path": base}) // only base_path
	r := httptest.NewRequest(http.MethodPost, "/settings", bytes.NewReader(body))
	w := httptest.NewRecorder()
	api.UpdateSettings(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	got := LoadSettings()
	require.True(t, got.AutoDeploy, "omitted auto_deploy must not disable deployments")
	require.Equal(t, "main", got.DeploySource, "omitted deploy_source must be preserved")
	require.Equal(t, []string{"alpha", "beta"}, got.WorkspaceFolders, "omitted workspace_folders must be preserved")

	// An explicit false is still honoured (a real operator pause).
	body, _ = json.Marshal(map[string]any{"auto_deploy": false})
	r = httptest.NewRequest(http.MethodPost, "/settings", bytes.NewReader(body))
	w = httptest.NewRecorder()
	api.UpdateSettings(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.False(t, LoadSettings().AutoDeploy, "explicit auto_deploy=false must be applied")
}
