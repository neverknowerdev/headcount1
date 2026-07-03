package endpoints

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// ListTaskSpecItems returns the acceptance criteria / test case items of a
// task (looked up on the root task of the subtask tree, where items live).
func (api *API) ListTaskSpecItems(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	root, err := api.q.GetRootTask(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "task not found")
		return
	}
	items, err := api.q.ListSpecItemsByTask(r.Context(), root.ID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, items)
}
