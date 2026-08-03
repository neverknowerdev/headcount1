package appsettings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// withHome points the settings loader at an isolated temp home for the test.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	// db.Headcount1Home() honours E2E_HEADCOUNT1_HOME (+ "/.headcount1").
	t.Setenv("E2E_HEADCOUNT1_HOME", home)
	base := filepath.Join(home, ".headcount1")
	require.NoError(t, os.MkdirAll(base, 0755))
	return base
}

func TestLoadDefaultsAutoDeployOn(t *testing.T) {
	withHome(t)
	// No settings file at all → deploys default to active on releases.
	s := Load()
	require.True(t, s.AutoDeploy, "AutoDeploy must default to true")
	require.Equal(t, DeploySourceReleases, s.EffectiveDeploySource())
}

func TestLoadPreservesExplicitAutoDeployFalse(t *testing.T) {
	base := withHome(t)
	// A file that lacks auto_deploy keeps the default (true)…
	require.NoError(t, os.WriteFile(filepath.Join(base, "settings.yaml"), []byte("deploy_source: main\n"), 0644))
	s := Load()
	require.True(t, s.AutoDeploy, "absent auto_deploy must not zero to false")
	require.Equal(t, DeploySourceMain, s.EffectiveDeploySource())

	// …but an explicit false is honoured.
	require.NoError(t, os.WriteFile(filepath.Join(base, "settings.yaml"), []byte("auto_deploy: false\n"), 0644))
	s = Load()
	require.False(t, s.AutoDeploy, "explicit auto_deploy: false must be preserved")
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withHome(t)
	require.NoError(t, Save(Settings{DeploySource: DeploySourceMain, AutoDeploy: false}))
	s := Load()
	require.Equal(t, DeploySourceMain, s.DeploySource)
	require.False(t, s.AutoDeploy)
}
