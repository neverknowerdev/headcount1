package endpoints

import (
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
	artifacts, err := api.q.ListArtifactsByTask(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, artifacts)
}
