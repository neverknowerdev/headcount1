package endpoints

import (
	"archive/zip"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func (api *API) ListTaskArtifacts(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	if _, err := api.authorizeTask(r, int32(id)); err != nil {
		api.respondError(w, http.StatusNotFound, "task not found")
		return
	}
	// Include subtask artifacts: delegated sessions produce artifacts on
	// their own task ids, but they belong to the main task's deliverables.
	artifacts, err := api.q.ListArtifactsByTaskTree(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, artifacts)
}

// DownloadArtifact streams one artifact as a file attachment.
func (api *API) DownloadArtifact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid artifact id")
		return
	}
	artifact, err := api.q.GetArtifact(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "artifact not found")
		return
	}
	if _, err := api.authorizeTask(r, artifact.TaskID); err != nil {
		api.respondError(w, http.StatusNotFound, "artifact not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", artifact.Filename))
	w.Write([]byte(artifact.Content))
}

// DownloadTaskArtifacts streams all artifacts of a task (including its
// subtasks') as one zip archive.
func (api *API) DownloadTaskArtifacts(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	if _, err := api.authorizeTask(r, int32(id)); err != nil {
		api.respondError(w, http.StatusNotFound, "task not found")
		return
	}
	artifacts, err := api.q.ListArtifactsByTaskTree(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(artifacts) == 0 {
		api.respondError(w, http.StatusNotFound, "task has no artifacts")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("task-%d-artifacts.zip", id)))
	zw := zip.NewWriter(w)
	defer zw.Close()
	seen := map[string]int{}
	for _, a := range artifacts {
		name := a.Filename
		// Same filename can appear across sessions — disambiguate.
		if seen[a.Filename] > 0 {
			name = fmt.Sprintf("%d-%s", a.ID, a.Filename)
		}
		seen[a.Filename]++
		f, zerr := zw.Create(name)
		if zerr != nil {
			return
		}
		f.Write([]byte(a.Content))
	}
}
