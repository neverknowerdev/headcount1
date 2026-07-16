package endpoints

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ListDefaultModelSettings returns the configured target (provider+model or
// model group) for every known internal-use purpose, e.g. commit-message
// generation and the ask_artifact one-shot reader.
func (api *API) ListDefaultModelSettings(w http.ResponseWriter, r *http.Request) {
	list, err := api.q.ListDefaultModelSettings(r.Context(), api.currentUserID(r))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, list)
}

// UpdateDefaultModelSetting sets a purpose's target. Exactly one of
// provider_id or model_group_id should be set (or neither, to clear the
// override and fall back to the calling session's own LLM) — provider_id
// takes precedence if both are somehow sent.
func (api *API) UpdateDefaultModelSetting(w http.ResponseWriter, r *http.Request) {
	purpose := chi.URLParam(r, "purpose")
	var req struct {
		ProviderID   *int32 `json:"provider_id"`
		Model        string `json:"model"`
		ModelGroupID *int32 `json:"model_group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	if req.ProviderID != nil {
		req.ModelGroupID = nil
	}
	updated, err := api.q.UpdateDefaultModelSetting(r.Context(), api.currentUserID(r), purpose, req.ProviderID, req.Model, req.ModelGroupID)
	if err != nil {
		api.respondError(w, http.StatusNotFound, "unknown purpose")
		return
	}
	api.respondJSON(w, http.StatusOK, updated)
}
