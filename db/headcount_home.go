package db

import (
	"os"
	"path/filepath"
)

// HeadcountHome returns the base path for headcount1 data.
// In E2E mode (E2E_HEADCOUNT_HOME is set), it returns that path + ".headcount1".
// Otherwise it returns ~/.headcount1.
func HeadcountHome() string {
	if e2eHome := os.Getenv("E2E_HEADCOUNT_HOME"); e2eHome != "" {
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
	return filepath.Join(HeadcountHome(), "settings.yaml")
}
