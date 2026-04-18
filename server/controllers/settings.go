package endpoints

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Settings struct {
	BasePath         string   `json:"base_path" yaml:"base_path"`
	WorkspaceFolders []string `json:"workspace_folders" yaml:"workspace_folders"`
}

func getSettingsFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/.paperclip2_settings.yaml"
	}
	return filepath.Join(homeDir, ".paperclip2_settings.yaml")
}

func LoadSettings() Settings {
	settingsPath := getSettingsFilePath()
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		homeDir, err := os.UserHomeDir()
		basePath := "/tmp/.paperclip2"
		if err == nil {
			basePath = filepath.Join(homeDir, ".paperclip2")
		}
		return Settings{BasePath: basePath, WorkspaceFolders: []string{}}
	}

	var settings Settings
	if err := yaml.Unmarshal(data, &settings); err != nil {
		homeDir, err := os.UserHomeDir()
		basePath := "/tmp/.paperclip2"
		if err == nil {
			basePath = filepath.Join(homeDir, ".paperclip2")
		}
		return Settings{BasePath: basePath, WorkspaceFolders: []string{}}
	}

	if settings.BasePath == "" {
		settings.BasePath = "/tmp/.paperclip2"
	}
	return settings
}

func SaveSettings(settings Settings) error {
	settingsPath := getSettingsFilePath()
	data, err := yaml.Marshal(&settings)
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, data, 0644)
}

func (api *API) GetSettings(w http.ResponseWriter, r *http.Request) {
	settings := LoadSettings()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

func (api *API) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var settings Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := SaveSettings(settings); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}
