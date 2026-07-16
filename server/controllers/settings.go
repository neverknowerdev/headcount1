package endpoints

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"agent-orchestrator/pkg/appsettings"
	"agent-orchestrator/pkg/filesystem"
)

type Settings = appsettings.Settings

type SSHKeyPayload struct {
	Key string `json:"key"`
}

func LoadSettings() Settings {
	return appsettings.Load()
}

func SaveSettings(settings Settings) error {
	return appsettings.Save(settings)
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

func (api *API) UploadSSHKey(w http.ResponseWriter, r *http.Request) {
	var payload SSHKeyPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	settings := LoadSettings()
	sshDir := filesystem.NewPaths(settings.BasePath).SSHDir()
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	keyPath := filepath.Join(sshDir, "id_rsa")
	if err := os.WriteFile(keyPath, []byte(payload.Key), 0600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
