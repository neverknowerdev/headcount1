package endpoints

import (
	"context"
	"encoding/json"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
)

// adoptClassifier puts a provider's System One model to its one use, the
// classifier slot, when that slot is still empty. A System One model can do
// nothing else, so a provider that brings one has brought the classifier:
// connecting it is then the whole setup. A slot somebody filled is left alone.
func (api *API) adoptClassifier(ctx context.Context, userID int32, provider db.LLMProvider) {
	if !provider.Enabled || provider.ApiKeyEncrypted == "" {
		return
	}
	candidates := provider.ModelsOfKind(models.ModelKindSystemOne)
	if len(candidates) == 0 {
		return
	}
	slot, err := api.q.GetDefaultModelSetting(ctx, userID, db.PurposeClassifier)
	if err != nil || slot.ProviderID != nil || slot.ModelGroupID != nil {
		return
	}
	// A free model costs nothing to have on; take one if there is one.
	model := candidates[0]
	for _, candidate := range candidates {
		if strings.Contains(strings.ToLower(candidate), "free") {
			model = candidate
			break
		}
	}
	if _, err := api.q.UpdateDefaultModelSetting(ctx, userID, db.PurposeClassifier, &provider.ID, model, nil); err == nil {
		api.engine.NotifyCredentialsChanged()
	}
}

// isSystemOneModel reports whether a model ID names a System One model.
func isSystemOneModel(id string) bool { return models.IsSystemOneModel(id) }

// systemOneTestProviderType is the provider type a successful test of a
// System One model reports: TypeSafe's own endpoint is a classifier and
// nothing else, while another provider stays what it was.
func systemOneTestProviderType(providerType, url string) string {
	switch {
	case providerType == "typesafe" || strings.Contains(strings.ToLower(url), "typesafe.ai"):
		return "typesafe"
	case providerType == "":
		return "openai"
	}
	return providerType
}

// invalidKeyMessage is what a connection test says when a provider turned the
// key away without saying why.
const invalidKeyMessage = "Invalid API Key or unauthorized access."

// providerErrorMessage reads what a provider said went wrong from an error
// body of either common shape: {"error":{"message":...}} or {"message":...}.
func providerErrorMessage(body []byte) string {
	var parsed struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	var nested struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(parsed.Error, &nested) == nil && strings.TrimSpace(nested.Message) != "" {
		return strings.TrimSpace(nested.Message)
	}
	var plain string
	if json.Unmarshal(parsed.Error, &plain) == nil && strings.TrimSpace(plain) != "" {
		return strings.TrimSpace(plain)
	}
	return strings.TrimSpace(parsed.Message)
}
