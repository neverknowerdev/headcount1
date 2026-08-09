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
	// BasePath (the server's filesystem layout) and WorkspaceFolders (a list
	// built from every tenant's company/project names) are instance-global and
	// must not leak to an ordinary self-registered user. Expose them only to the
	// operator, mirroring the gate on UpdateSettings.
	admin := api.isInstanceAdmin(r.Context(), api.currentUserID(r))
	w.Header().Set("Content-Type", "application/json")
	if !admin {
		// Deployment configuration is instance-global and must not be disclosed
		// to ordinary users through this shared settings endpoint. Omit the keys
		// entirely rather than returning zero values that look like real settings.
		json.NewEncoder(w).Encode(map[string]any{
			"base_path":         "",
			"workspace_folders": nil,
			"git_remote_url":    settings.GitRemoteURL,
		})
		return
	}
	json.NewEncoder(w).Encode(settings)
}

func (api *API) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	// Decode ON TOP of the current settings so a caller that sends only some
	// fields doesn't silently reset the rest. Decoding into a zero value would
	// wipe workspace_folders and — because AutoDeploy defaults to true — turn
	// an omitted auto_deploy into "deploys disabled".
	settings := LoadSettings()
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := SaveSettings(settings); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Deploy settings (deploy_source, auto_deploy) are read fresh from the file
	// by the deploy webhook on each event, so there's no live updater state to
	// propagate here — saving is enough.

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
