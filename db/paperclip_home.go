package db

import (
	"os"
	"path/filepath"
)

// PaperclipHome returns the base path for paperclip2 data.
// In E2E mode (E2E_PAPERCLIP_HOME is set), it returns that path + ".paperclip2".
// Otherwise it returns ~/.paperclip2.
func PaperclipHome() string {
	if e2eHome := os.Getenv("E2E_PAPERCLIP_HOME"); e2eHome != "" {
		return filepath.Join(e2eHome, ".paperclip2")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/.paperclip2"
	}
	return filepath.Join(homeDir, ".paperclip2")
}

// SettingsFilePath returns the path to the settings.yaml file.
func SettingsFilePath() string {
	return filepath.Join(PaperclipHome(), "settings.yaml")
}

// OpencodeConfigDir returns the opencode config directory.
// In E2E mode (E2E_PAPERCLIP_HOME is set), it returns that path + ".config/opencode".
// Otherwise it returns ~/.config/opencode.
func OpencodeConfigDir() string {
	if e2eHome := os.Getenv("E2E_PAPERCLIP_HOME"); e2eHome != "" {
		return filepath.Join(e2eHome, ".config", "opencode")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/.config/opencode"
	}
	return filepath.Join(homeDir, ".config", "opencode")
}
