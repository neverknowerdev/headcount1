package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

type Settings struct {
	BasePath string `json:"base_path"`
}

func getSettingsFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/.paperclip2_settings.json"
	}
	return filepath.Join(homeDir, ".paperclip2_settings.json")
}

func LoadSettings() Settings {
	settingsPath := getSettingsFilePath()
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		// return default
		homeDir, err := os.UserHomeDir()
		basePath := "/tmp/.paperclip2"
		if err == nil {
			basePath = filepath.Join(homeDir, ".paperclip2")
		}
		return Settings{BasePath: basePath}
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		// return default
		homeDir, err := os.UserHomeDir()
		basePath := "/tmp/.paperclip2"
		if err == nil {
			basePath = filepath.Join(homeDir, ".paperclip2")
		}
		return Settings{BasePath: basePath}
	}

	if settings.BasePath == "" {
		return Settings{BasePath: "/tmp/.paperclip2"}
	}
	return settings
}

func SaveSettings(settings Settings) error {
	settingsPath := getSettingsFilePath()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, data, 0644)
}

func registerSettingsRoutes(r chi.Router, db *gorm.DB) {
	r.Get("/settings", func(w http.ResponseWriter, r *http.Request) {
		settings := LoadSettings()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(settings)
	})

	r.Post("/settings", func(w http.ResponseWriter, r *http.Request) {
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
	})
}
