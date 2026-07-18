package endpoints

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/llmdiscovery"
	"agent-orchestrator/pkg/secrets"
	"agent-orchestrator/pkg/utils"
)

func (api *API) ListProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := api.q.ListLLMProvidersForUser(r.Context(), api.currentUserID(r))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, providers)
}

// ListProviderPresets returns the known provider presets (OpenCode Go,
// MiniMax, ...) so the frontend can offer them in an "Add Provider" dropdown
// — the user only needs to pick one and paste in an API key.
func (api *API) ListProviderPresets(w http.ResponseWriter, r *http.Request) {
	presets, err := api.q.ListProviderPresets(r.Context())
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, presets)
}

// CreateProviderFromPreset creates a provider from a known preset and the
// user's own API key. Unlike the manual "Add Provider" form, this runs no
// separate test-connection probe: a successful model-catalog fetch (using
// the supplied key) is itself sufficient validation, so the provider is
// only created if that fetch succeeds.
func (api *API) CreateProviderFromPreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PresetKey string `json:"preset_key"`
		ApiKey    string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	if req.ApiKey == "" {
		api.respondError(w, http.StatusBadRequest, "API key is required")
		return
	}

	preset, err := api.q.GetProviderPresetByKey(r.Context(), req.PresetKey)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "unknown provider preset")
		return
	}

	client := &http.Client{Timeout: 20 * time.Second}
	models, err := llmdiscovery.FetchModelsForPreset(r.Context(), client, preset.Key, preset.BaseUrl, req.ApiKey)
	if err != nil {
		api.respondError(w, http.StatusBadGateway, "failed to fetch models — check your API key: "+err.Error())
		return
	}
	if len(models) == 0 {
		api.respondError(w, http.StatusBadGateway, "provider returned no models")
		return
	}

	uid := api.currentUserID(r)
	sealedKey, err := secrets.Default().EncryptForUser(uid, req.ApiKey)
	if err != nil {
		api.respondError(w, http.StatusConflict, "vault is locked — re-authenticate to save an API key")
		return
	}
	p := db.LLMProvider{
		Name:            preset.Name,
		BaseUrl:         preset.BaseUrl,
		ApiKeyEncrypted: sealedKey,
		UserID:          &uid,
		ProviderType:    preset.ProviderType,
		DefaultModel:    models[0],
		SupportedModels: strings.Join(models, ","),
		PresetKey:       preset.Key,
		Enabled:         true,
	}
	if err := api.db.Create(&p).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.HasApiKey = p.ApiKeyEncrypted != ""
	api.respondJSON(w, http.StatusCreated, p)
}

func (api *API) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	provider := api.providerFromCtx(r) // loaded + authorized by loadProvider
	if provider.Builtin {
		// Deleting a builtin provider is pointless anyway — EnsureBuiltinLLMProvidersForUser
		// just recreates it (blank) on the next startup. Disable it instead.
		api.respondError(w, http.StatusForbidden, "built-in providers cannot be deleted — disable it instead")
		return
	}

	err := api.q.DeleteLLMProvider(r.Context(), provider.ID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (api *API) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"name"`
		BaseUrl         string `json:"base_url"`
		ApiKey          string `json:"api_key"`
		ProviderType    string `json:"provider_type"`
		DefaultModel    string `json:"default_model"`
		SupportedModels string `json:"supported_models"`
		// Enabled is a pointer so a caller that omits it (an older client, or
		// a request that only means to touch other fields) leaves the
		// current value untouched instead of silently disabling the provider.
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}

	provider := api.providerFromCtx(r) // loaded + authorized by loadProvider

	provider.Name = req.Name
	provider.BaseUrl = req.BaseUrl
	provider.ProviderType = req.ProviderType
	provider.DefaultModel = req.DefaultModel
	provider.SupportedModels = req.SupportedModels
	if req.ApiKey != "" {
		uid := api.currentUserID(r)
		sealedKey, err := secrets.Default().EncryptForUser(uid, req.ApiKey)
		if err != nil {
			api.respondError(w, http.StatusConflict, "vault is locked — re-authenticate to change the API key")
			return
		}
		provider.ApiKeyEncrypted = sealedKey
	}
	if req.Enabled != nil {
		provider.Enabled = *req.Enabled
	}

	updated, err := api.q.UpdateLLMProvider(r.Context(), provider)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, updated)
}

// RediscoverProviderModels re-fetches a provider's model catalog on demand —
// either a builtin free provider's public catalog, or a preset-derived
// provider's authenticated catalog (using its saved API key). Unlike the
// automatic background/daily refresh (which never overwrites an
// already-set DefaultModel), this always sets the default to the top of
// the freshly fetched list — the whole point of a user explicitly asking
// to re-discover models is to get the current best/full pick.
func (api *API) RediscoverProviderModels(w http.ResponseWriter, r *http.Request) {
	provider := api.providerFromCtx(r) // loaded + authorized by loadProvider

	client := &http.Client{Timeout: 20 * time.Second}
	var models []string
	var err error
	switch {
	case provider.Builtin:
		switch provider.ProviderName {
		case db.ProviderVendorOpenRouter:
			models, err = llmdiscovery.FetchOpenRouterFreeModels(r.Context(), client)
		case db.ProviderVendorOpenCodeZen:
			models, err = llmdiscovery.FetchOpenCodeZenFreeModels(r.Context(), client)
		default:
			api.respondError(w, http.StatusBadRequest, "unrecognized built-in provider")
			return
		}
	case provider.PresetKey != "":
		if provider.ApiKeyEncrypted == "" {
			api.respondError(w, http.StatusBadRequest, "add an API key before re-discovering models")
			return
		}
		apiKey, decErr := secrets.Default().Decrypt(provider.ApiKeyEncrypted)
		if decErr != nil {
			api.respondError(w, http.StatusConflict, "vault is locked — re-authenticate to re-discover models")
			return
		}
		models, err = llmdiscovery.FetchModelsForPreset(r.Context(), client, provider.PresetKey, provider.BaseUrl, apiKey)
	default:
		api.respondError(w, http.StatusBadRequest, "only built-in or preset-based providers support model re-discovery")
		return
	}
	if err != nil {
		api.respondError(w, http.StatusBadGateway, "failed to fetch models: "+err.Error())
		return
	}
	if len(models) == 0 {
		api.respondError(w, http.StatusBadGateway, "provider returned no models")
		return
	}

	if err := api.q.ForceUpdateLLMProviderModelCatalog(r.Context(), provider.ID, models); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := api.q.GetLLMProvider(r.Context(), provider.ID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, updated)
}

func (api *API) CreateProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"name"`
		BaseUrl         string `json:"base_url"`
		ApiKey          string `json:"api_key"`
		ProviderType    string `json:"provider_type"`
		DefaultModel    string `json:"default_model"`
		SupportedModels string `json:"supported_models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	uid := api.currentUserID(r)
	sealedKey, err := secrets.Default().EncryptForUser(uid, req.ApiKey)
	if err != nil {
		api.respondError(w, http.StatusConflict, "vault is locked — re-authenticate to save an API key")
		return
	}
	p := db.LLMProvider{
		Name:            req.Name,
		BaseUrl:         req.BaseUrl,
		ApiKeyEncrypted: sealedKey,
		UserID:          &uid,
		ProviderType:    req.ProviderType,
		DefaultModel:    req.DefaultModel,
		SupportedModels: req.SupportedModels,
		Enabled:         true,
	}
	if err := api.db.Create(&p).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.HasApiKey = p.ApiKeyEncrypted != ""
	api.respondJSON(w, http.StatusCreated, p)
}

func (api *API) TestProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderID   *int32 `json:"provider_id"`
		BaseUrl      string `json:"base_url"`
		ApiKey       string `json:"api_key"`
		Model        string `json:"model"`
		ProviderType string `json:"provider_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}

	apiKey := req.ApiKey
	baseUrl := req.BaseUrl
	providerType := req.ProviderType

	if req.ProviderID != nil && (apiKey == "" || baseUrl == "" || providerType == "") {
		provider, err := api.q.GetLLMProvider(r.Context(), *req.ProviderID)
		if err == nil && ownedByUser(r, provider.UserID) {
			if apiKey == "" {
				// Decrypt the saved key at the point of use; a locked vault
				// simply leaves apiKey empty and the 400 below asks for one.
				apiKey, _ = secrets.Default().Decrypt(provider.ApiKeyEncrypted)
			}
			if baseUrl == "" {
				baseUrl = provider.BaseUrl
			}
			if providerType == "" {
				providerType = provider.ProviderType
			}
		}
	}

	if apiKey == "" {
		api.respondError(w, http.StatusBadRequest, "API Key is required to test a provider")
		return
	}

	url := strings.TrimSpace(baseUrl)

	// Helper to make request
	makeRequest := func(reqUrl string, isAnthropic bool) (int, string, string, error) {
		var payload []byte
		var clientReq *http.Request
		var err error

		if isAnthropic {
			p := map[string]interface{}{
				"model": req.Model,
				"messages": []map[string]string{
					{"role": "user", "content": "Say 'hello world'"},
				},
				"max_tokens": 10,
			}
			payload, _ = json.Marshal(p)
		} else {
			p := map[string]interface{}{
				"model": req.Model,
				"messages": []map[string]string{
					{"role": "user", "content": "Say 'hello world'"},
				},
				"max_tokens": 10,
			}
			payload, _ = json.Marshal(p)
		}

		clientReq, err = http.NewRequest("POST", reqUrl, bytes.NewBuffer(payload))
		if err != nil {
			return 0, "", "", err
		}

		clientReq.Header.Set("Content-Type", "application/json")
		if isAnthropic {
			clientReq.Header.Set("x-api-key", apiKey)
			clientReq.Header.Set("anthropic-version", "2023-06-01")
		} else {
			clientReq.Header.Set("Authorization", "Bearer "+apiKey)
		}

		client := &http.Client{Timeout: 30 * time.Second} // Increased timeout
		resp, err := client.Do(clientReq)
		if err != nil {
			return 0, "", "", err
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)

		bodyStr := string(respBody)
		if strings.Contains(strings.ToLower(bodyStr), "<html") {
			bodyStr = "<HTML response omitted>"
		}

		logMsg := "Request URL: " + reqUrl + "\nStatus: " + resp.Status + "\nResponse: " + bodyStr

		var parsedErr string
		if resp.StatusCode >= 400 {
			lowerBodyStr := strings.ToLower(string(respBody))
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				parsedErr = "Invalid API Key or unauthorized access."
			} else if resp.StatusCode == 429 {
				parsedErr = "Rate limit exceeded or insufficient quota."
			} else if strings.Contains(lowerBodyStr, "model") && (strings.Contains(lowerBodyStr, "not found") || strings.Contains(lowerBodyStr, "does not exist") || strings.Contains(lowerBodyStr, "invalid") || strings.Contains(lowerBodyStr, "unsupported")) {
				parsedErr = "Model is not supported or not found."
			} else if resp.StatusCode == 404 {
				if strings.Contains(lowerBodyStr, "model") {
					parsedErr = "Model is not supported or not found."
				} else {
					parsedErr = "Endpoint not found (404). Please check the Base URL."
				}
			} else {
				var errJson map[string]interface{}
				if err := json.Unmarshal(respBody, &errJson); err == nil {
					if errObj, ok := errJson["error"].(map[string]interface{}); ok {
						if msg, ok := errObj["message"].(string); ok {
							parsedErr = msg
						}
					}
				}
				if parsedErr == "" {
					parsedErr = "Provider returned error status: " + resp.Status
				}
			}
		}

		return resp.StatusCode, logMsg, parsedErr, nil
	}

	var openAiUrls []string
	var anthropicUrls []string

	openAiUrls = append(openAiUrls, utils.BuildProviderURL(url, "/chat/completions"))
	anthropicUrls = append(anthropicUrls, utils.BuildProviderURL(url, "/messages"))

	// Channel to receive the first successful result
	type TestResult struct {
		isSuccess    bool
		status       int
		logMsg       string
		parsedErr    string
		err          error
		testUrl      string
		providerType string
	}

	resultCh := make(chan TestResult, len(openAiUrls)+len(anthropicUrls))

	for _, testUrl := range openAiUrls {
		go func(u string) {
			status, logMsg, parsedErr, err := makeRequest(u, false)
			res := TestResult{
				isSuccess:    err == nil && status >= 200 && status < 300,
				status:       status,
				logMsg:       "--- OpenAI Format Attempt (" + u + ") ---\n" + logMsg,
				parsedErr:    parsedErr,
				err:          err,
				testUrl:      u,
				providerType: "openai",
			}
			if err != nil {
				res.logMsg = "--- OpenAI Format Attempt (" + u + ") ---\nError: " + err.Error()
			}
			resultCh <- res
		}(testUrl)
	}

	for _, testUrl := range anthropicUrls {
		go func(u string) {
			status, logMsg, parsedErr, err := makeRequest(u, true)
			res := TestResult{
				isSuccess:    err == nil && status >= 200 && status < 300,
				status:       status,
				logMsg:       "--- Anthropic Format Attempt (" + u + ") ---\n" + logMsg,
				parsedErr:    parsedErr,
				err:          err,
				testUrl:      u,
				providerType: "anthropic",
			}
			if err != nil {
				res.logMsg = "--- Anthropic Format Attempt (" + u + ") ---\nError: " + err.Error()
			}
			resultCh <- res
		}(testUrl)
	}

	totalRequests := len(openAiUrls) + len(anthropicUrls)
	var combinedLog string
	var lastParsedErr string

	for i := 0; i < totalRequests; i++ {
		res := <-resultCh
		combinedLog += res.logMsg + "\n\n"

		if res.isSuccess {
			// Short circuit on first success
			api.respondJSON(w, http.StatusOK, map[string]interface{}{
				"status":        "ok",
				"provider_type": res.providerType,
				"url":           res.testUrl,
				"log":           combinedLog,
			})
			return
		}
		if res.parsedErr != "" {
			lastParsedErr = res.parsedErr
		}
	}

	finalErr := lastParsedErr
	if finalErr == "" {
		finalErr = "Provider returned error for all attempted URLs"
	}

	api.respondJSON(w, http.StatusBadRequest, map[string]interface{}{"error": finalErr, "log": combinedLog})
}
