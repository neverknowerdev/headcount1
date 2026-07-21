package endpoints

import (
	"fmt"
	"net/http"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/backup"
)

// ExportMyData streams a tenant-scoped archive of the signed-in user's own
// subtree (their companies/projects/tasks/agents/providers and the on-disk
// files under them). Unlike the operator-only whole-DB backup, this contains
// only the caller's data and can be re-imported into any database — including
// one that already has other tenants — via ImportMyData.
func (api *API) ExportMyData(w http.ResponseWriter, r *http.Request) {
	userID := api.currentUserID(r)
	if userID == 0 {
		api.respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	basePath := backupBasePath()
	filename := fmt.Sprintf("headcount1-export_%s.tar.gz", time.Now().Format("2006-01-02_150405"))

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	if err := backup.ExportTenant(r.Context(), w, basePath, api.db, userID); err != nil {
		// Headers may already be flushed; log and best-effort signal.
		http.Error(w, "export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

// ImportMyData imports a tenant-scoped archive (multipart form field "file")
// into the current user's account, assigning fresh IDs, rewriting every foreign
// key and on-disk path, and re-owning the whole subtree to the caller and their
// team. Other tenants are untouched.
func (api *API) ImportMyData(w http.ResponseWriter, r *http.Request) {
	userID := api.currentUserID(r)
	if userID == 0 {
		api.respondError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	// Resolve the importer's team so the restored subtree is bound to it.
	membership, err := api.q.GetTeamMembership(r.Context(), userID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "could not resolve team membership")
		return
	}

	// 512 MiB cap on the uploaded archive.
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid upload: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "missing 'file' upload")
		return
	}
	defer file.Close()

	basePath := backupBasePath()
	stats, err := backup.ImportTenantFromReader(r.Context(), file, basePath, api.db, userID, membership.TeamID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "import failed: "+err.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Data imported successfully",
		"result":  stats,
	})
}

// backupBasePath resolves the runtime data root the same way the other backup
// handlers do.
func backupBasePath() string {
	settings := LoadSettings()
	if settings.BasePath != "" {
		return settings.BasePath
	}
	return db.Headcount1Home()
}
