// Package appsettings is the single loader/saver for the app-wide
// settings.yaml. It is a leaf package so main, server and engine can all
// resolve BasePath through the same code path (main needs it before the DB
// is opened).
package appsettings

import (
	"os"
	"path/filepath"

	"agent-orchestrator/db"

	"gopkg.in/yaml.v3"
)

type Settings struct {
	BasePath         string   `json:"base_path" yaml:"base_path"`
	WorkspaceFolders []string `json:"workspace_folders" yaml:"workspace_folders"`
	// Deploy configuration (app-global). Deploys are pushed to this server by
	// CI as authenticated webhook events; these settings decide which events a
	// PRODUCTION server acts on (staging accepts any branch — see the deploy
	// controller):
	//   - DeploySource: "releases" (default) applies published-release events;
	//     "main" applies main-branch push events instead.
	//   - AutoDeploy: master switch (default true). When false, matching events
	//     are recorded/acknowledged but not applied — an operator pause.
	DeploySource string `json:"deploy_source" yaml:"deploy_source"`
	AutoDeploy   bool   `json:"auto_deploy" yaml:"auto_deploy"`
}

// Deploy source values.
const (
	DeploySourceReleases = "releases"
	DeploySourceMain     = "main"
)

// EffectiveDeploySource returns DeploySource with the default applied.
func (s Settings) EffectiveDeploySource() string {
	if s.DeploySource == DeploySourceMain {
		return DeploySourceMain
	}
	return DeploySourceReleases
}

// Load reads settings.yaml from its bootstrap location (db.Headcount1Home()).
// It never fails: a missing or unreadable file yields defaults with
// BasePath = Headcount1Home().
func Load() Settings {
	// Start from defaults and unmarshal ON TOP: yaml.v3 leaves fields absent
	// from the file untouched, so AutoDeploy defaults to true (deploys active)
	// unless the file explicitly sets it false.
	defaults := func() Settings {
		return Settings{
			BasePath:         db.Headcount1Home(),
			WorkspaceFolders: []string{},
			DeploySource:     DeploySourceReleases,
			AutoDeploy:       true,
		}
	}

	data, err := os.ReadFile(db.SettingsFilePath())
	if err != nil {
		return defaults()
	}

	settings := defaults()
	if err := yaml.Unmarshal(data, &settings); err != nil {
		return defaults()
	}
	if settings.BasePath == "" {
		settings.BasePath = db.Headcount1Home()
	}
	if settings.WorkspaceFolders == nil {
		settings.WorkspaceFolders = []string{}
	}
	return settings
}

// Save writes settings.yaml to its bootstrap location.
func Save(settings Settings) error {
	settingsPath := db.SettingsFilePath()
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(&settings)
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, data, 0644)
}
