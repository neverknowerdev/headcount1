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
	// MemoryRecallMaxTokens / MemoryBriefingMaxTokens override the memory
	// layer's default recall token budgets (0 = use the built-in defaults:
	// 6144 for the memory_recall tool, 4096 for the automatic pre-task
	// briefing — see pkg/hindsight defaultRecallMaxTokens/defaultBriefingMaxTokens).
	MemoryRecallMaxTokens   int `json:"memory_recall_max_tokens" yaml:"memory_recall_max_tokens"`
	MemoryBriefingMaxTokens int `json:"memory_briefing_max_tokens" yaml:"memory_briefing_max_tokens"`
	// ServiceUser is an existing unprivileged OS account that long-running
	// child processes (currently the memory backend) run under, so the agent
	// sandbox cannot read their environment via /proc/<pid>/environ — and so
	// hindsight's embedded Postgres has a non-root uid when the app runs as
	// root. Empty = run children as this process. The account is never created
	// by the app; see doc/hindsight-hardening.md. Env override:
	// HEADCOUNT1_SERVICE_USER.
	ServiceUser string `json:"service_user" yaml:"service_user"`
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
