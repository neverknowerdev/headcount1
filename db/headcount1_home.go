package db

import (
	"os"
	"path/filepath"
)

// Headcount1Home returns the base path for headcount1 data.
// In E2E mode (E2E_HEADCOUNT1_HOME is set), it returns that path + ".headcount1".
// Otherwise it returns ~/.headcount1.
func Headcount1Home() string {
	if e2eHome := os.Getenv("E2E_HEADCOUNT1_HOME"); e2eHome != "" {
		return filepath.Join(e2eHome, ".headcount1")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/.headcount1"
	}
	return filepath.Join(homeDir, ".headcount1")
}

// SettingsFilePath returns the path to the settings.yaml file.
func SettingsFilePath() string {
	return filepath.Join(Headcount1Home(), "settings.yaml")
}
