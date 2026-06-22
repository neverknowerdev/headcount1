package endpoints

import (
	"net/http"
)

func (api *API) GetVersion(w http.ResponseWriter, r *http.Request) {
	if api.updater == nil {
		api.respondJSON(w, http.StatusOK, map[string]string{
			"branch":      "dev",
			"commit_hash": "unknown",
			"build_date":  "unknown",
			"display":     "dev",
		})
		return
	}
	status := api.updater.GetStatus()
	v := status.Current
	api.respondJSON(w, http.StatusOK, map[string]string{
		"branch":      v.Branch,
		"commit_hash": v.CommitHash,
		"build_date":  v.BuildDate,
		"display":     v.DisplayString(),
	})
}

func (api *API) GetUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if api.updater == nil {
		api.respondJSON(w, http.StatusOK, map[string]interface{}{"error": "updater not available"})
		return
	}
	api.respondJSON(w, http.StatusOK, api.updater.GetStatus())
}

func (api *API) CheckForUpdate(w http.ResponseWriter, r *http.Request) {
	if api.updater == nil {
		http.Error(w, "updater not available", http.StatusServiceUnavailable)
		return
	}

	settings := LoadSettings()
	if settings.UpdateBranch != "" {
		api.updater.SetTrackedBranch(settings.UpdateBranch)
	}

	if err := api.updater.CheckForUpdate(); err != nil {
		// Return status even on error so client can see the error message
		api.respondJSON(w, http.StatusOK, api.updater.GetStatus())
		return
	}

	api.respondJSON(w, http.StatusOK, api.updater.GetStatus())
}

func (api *API) ApplyUpdate(w http.ResponseWriter, r *http.Request) {
	if api.updater == nil {
		http.Error(w, "updater not available", http.StatusServiceUnavailable)
		return
	}

	if err := api.updater.ApplyUpdate(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	api.respondJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}
