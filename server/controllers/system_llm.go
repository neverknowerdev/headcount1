package endpoints

import (
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// ListSystemLLMLogs serves the Run Logs UI's "System LLM calls" view:
// non-session LLM calls (memory layer, artifact Q&A, commit messages),
// newest first. Query params: source, limit, offset.
func (api *API) ListSystemLLMLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	source := r.URL.Query().Get("source")
	logs, total, err := api.q.ListSystemLLMLogs(r.Context(), source, limit, offset)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]interface{}{"items": logs, "total": total})
}

// GetSystemLLMLog returns one call's metadata plus the full request/response
// bodies from its filesystem log file (data/logs/system-llm/...).
func (api *API) GetSystemLLMLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	entry, err := api.q.GetSystemLLMLog(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "log not found")
		return
	}
	resp := map[string]interface{}{"log": entry}
	if entry.LogFilePath != "" {
		if data, rerr := os.ReadFile(entry.LogFilePath); rerr == nil {
			resp["file"] = string(data)
		}
	}
	api.respondJSON(w, http.StatusOK, resp)
}
