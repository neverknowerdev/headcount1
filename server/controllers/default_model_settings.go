package endpoints

import (
	"encoding/json"
	"net/http"

	"agent-orchestrator/db"

	"agent-orchestrator/db/models"
	"context"
	"strings"

	"github.com/go-chi/chi/v5"
)

// ListDefaultModelSettings returns the configured target (provider+model or
// model group) of every model slot: smart, cheap, classifier and commit
// messages.
func (api *API) ListDefaultModelSettings(w http.ResponseWriter, r *http.Request) {
	list, err := api.q.ListDefaultModelSettings(r.Context(), api.currentUserID(r))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, list)
}

// UpdateDefaultModelSetting sets a slot's target. Exactly one of provider_id
// or model_group_id should be set, or neither to leave the slot unconfigured;
// provider_id takes precedence if both are sent.
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
	// Both IDs are per-user and sequential; purpose resolution later decrypts
	// the referenced provider by its embedded owner, so bind only the caller's
	// own provider/group here.
	if err := api.authorizeModelBinding(r, req.ProviderID, req.ModelGroupID); err != nil {
		api.respondError(w, http.StatusNotFound, "provider or model group not found")
		return
	}
	// The classifier slot takes a System One model, or a group of them, and
	// nothing else; a System One model can do none of the work the other
	// slots are for.
	if req.ProviderID != nil || req.ModelGroupID != nil {
		kind, err := api.modelChoiceKind(r.Context(), req.ProviderID, req.ModelGroupID, req.Model)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "provider or model group not found")
			return
		}
		if purpose == db.PurposeClassifier && kind != models.ModelKindSystemOne {
			api.respondError(w, http.StatusBadRequest, "the classifier slot needs a System One model such as Jev, or a group of them; a language model cannot fill it")
			return
		}
		if purpose != db.PurposeClassifier && kind == models.ModelKindSystemOne {
			api.respondError(w, http.StatusBadRequest, "a System One model is a classifier, not a language model; it only fits the classifier slot")
			return
		}
	}
	updated, err := api.q.UpdateDefaultModelSetting(r.Context(), api.currentUserID(r), purpose, req.ProviderID, req.Model, req.ModelGroupID)
	if err != nil {
		api.respondError(w, http.StatusNotFound, "unknown purpose")
		return
	}
	// Tasks that were waiting for a model of this tier can go on.
	api.engine.NotifyCredentialsChanged()
	api.respondJSON(w, http.StatusOK, updated)
}

// modelChoiceKind is the kind of the model a choice names: that of a group,
// or of a provider's model. A provider named without a model stands for its
// default model.
func (api *API) modelChoiceKind(ctx context.Context, providerID, modelGroupID *int32, model string) (string, error) {
	if modelGroupID != nil {
		group, err := api.q.GetModelGroup(ctx, *modelGroupID)
		if err != nil {
			return "", err
		}
		if models.IsModelKind(group.Kind) {
			return group.Kind, nil
		}
		return models.ModelKindLLM, nil
	}
	if providerID == nil {
		return models.ModelKindLLM, nil
	}
	provider, err := api.q.GetLLMProvider(ctx, *providerID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(model) == "" {
		model = provider.DefaultModel
	}
	return models.ModelKind(model), nil
}

// authorizeModelBinding verifies that a provider and a model group belong to
// the caller. Both are per-user and keyed by sequential ids, and a provider's
// key is decrypted by the owner embedded in its ciphertext, so without this
// check a slot or a task could point at another tenant's provider and spend
// their API key. Nil bindings are fine.
func (api *API) authorizeModelBinding(r *http.Request, providerID, modelGroupID *int32) error {
	if providerID != nil {
		if _, err := api.authorizeProvider(r, *providerID); err != nil {
			return err
		}
	}
	if modelGroupID != nil {
		if _, err := api.authorizeModelGroup(r, *modelGroupID); err != nil {
			return err
		}
	}
	return nil
}
