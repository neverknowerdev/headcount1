package endpoints

import (
	"context"
	"net/http"

	"agent-orchestrator/pkg/filesystem"
)

func (api *API) SyncDBWithFilesystem(ctx context.Context) error {
	settings := LoadSettings()
	fm := filesystem.NewManager(settings.BasePath)
	if err := fm.SetupBaseDirectories(); err != nil {
		return err
	}
	return nil
}

func (api *API) SyncSettings(w http.ResponseWriter, r *http.Request) {
	err := api.SyncDBWithFilesystem(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
