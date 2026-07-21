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
	// Auto-update configuration (app-global). UpdateBranch is the branch whose
	// CI release the running binary tracks; UpdateCheckIntervalMins is the poll
	// cadence (0 → updater default of 60m).
	UpdateBranch            string `json:"update_branch" yaml:"update_branch"`
	AutoUpdate              bool   `json:"auto_update" yaml:"auto_update"`
	UpdateCheckIntervalMins int    `json:"update_check_interval_mins" yaml:"update_check_interval_mins"`
}

// Load reads settings.yaml from its bootstrap location (db.Headcount1Home()).
// It never fails: a missing or unreadable file yields defaults with
// BasePath = Headcount1Home().
func Load() Settings {
	defaults := Settings{BasePath: db.Headcount1Home(), WorkspaceFolders: []string{}}

	data, err := os.ReadFile(db.SettingsFilePath())
	if err != nil {
		return defaults
	}

	var settings Settings
	if err := yaml.Unmarshal(data, &settings); err != nil {
		return defaults
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
