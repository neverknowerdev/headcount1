package endpoints

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// GetTaskSessions returns all sessions for a task, ordered by creation time.
func (api *API) GetTaskSessions(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	sessions, err := api.q.GetTaskSessions(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, sessions)
}

// GetSession returns a single session by ID.
func (api *API) GetSession(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	session, err := api.q.GetSession(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, session)
}
