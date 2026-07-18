package endpoints

import (
	"encoding/json"
	"net/http"
	"strings"

	"agent-orchestrator/pkg/appsettings"
	"agent-orchestrator/pkg/secrets"
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

// UploadSSHKey stores the caller's own git SSH private key, encrypted at rest
// under their per-user DEK (like provider keys). It is per-user: one account can
// no longer overwrite a shared identity. Requires the vault to be unlocked so
// the key can be sealed.
func (api *API) UploadSSHKey(w http.ResponseWriter, r *http.Request) {
	var payload SSHKeyPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(payload.Key) == "" {
		api.respondError(w, http.StatusBadRequest, "key is required")
		return
	}
	uid := api.currentUserID(r)
	if !secrets.IsUnlocked(uid) {
		api.respondError(w, http.StatusConflict, "vault locked — unlock with your passkey before saving a key")
		return
	}
	if err := api.q.UpsertUserSSHKey(r.Context(), uid, payload.Key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
