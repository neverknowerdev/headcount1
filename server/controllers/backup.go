package endpoints

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/backup"
)

type BackupStatus struct {
	LatestBackup *string  `json:"latest_backup"`
	Backups      []string `json:"backups"`
}

type RestoreRequest struct {
	ArchivePath string `json:"archive_path"`
}

func (api *API) CreateBackup(w http.ResponseWriter, r *http.Request) {
	settings := LoadSettings()
	basePath := settings.BasePath
	if basePath == "" {
		basePath = db.PaperclipHome()
	}

	archivePath, err := backup.CreateBackup(basePath)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "Failed to create backup: "+err.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, map[string]string{
		"archive_path": archivePath,
		"message":      "Backup created successfully",
	})
}

func (api *API) GetBackupStatus(w http.ResponseWriter, r *http.Request) {
	settings := LoadSettings()
	basePath := settings.BasePath
	if basePath == "" {
		basePath = db.PaperclipHome()
	}

	latest, err := backup.GetLatestBackup(basePath)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "Failed to check backup status")
		return
	}

	backups, err := backup.ListBackups(basePath)
	if err != nil {
		backups = []string{}
	}

	status := BackupStatus{
		Backups: backups,
	}
	if latest != nil {
		latestStr := latest.Format("2006-01-02 15:04:05 UTC")
		status.LatestBackup = &latestStr
	}

	api.respondJSON(w, http.StatusOK, status)
}

func (api *API) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	var req RestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	settings := LoadSettings()
	basePath := settings.BasePath
	if basePath == "" {
		basePath = db.PaperclipHome()
	}

	// Resolve archive path
	archivePath := req.ArchivePath
	if !filepath.IsAbs(archivePath) {
		archivePath = filepath.Join(basePath, "backups", archivePath)
	}

	if _, err := os.Stat(archivePath); os.IsNotExist(err) {
		api.respondError(w, http.StatusNotFound, "Archive not found: "+archivePath)
		return
	}

	err := backup.RestoreBackup(archivePath, basePath, api.db)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "Failed to restore backup: "+err.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Backup restored successfully",
	})
}

func (api *API) ListBackups(w http.ResponseWriter, r *http.Request) {
	settings := LoadSettings()
	basePath := settings.BasePath
	if basePath == "" {
		basePath = db.PaperclipHome()
	}

	backups, err := backup.ListBackups(basePath)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "Failed to list backups")
		return
	}

	api.respondJSON(w, http.StatusOK, map[string]interface{}{
		"backups": backups,
	})
}
