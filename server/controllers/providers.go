package endpoints

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agent-orchestrator/db"
	"github.com/go-chi/chi/v5"
)

func (api *API) ListProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := api.q.ListLLMProviders(r.Context())
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, providers)
}

func (api *API) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	err = api.q.DeleteLLMProvider(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (api *API) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

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

	provider, err := api.q.GetLLMProvider(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "provider not found")
		return
	}

	provider.Name = req.Name
	provider.BaseUrl = req.BaseUrl
	provider.ProviderType = req.ProviderType
	provider.DefaultModel = req.DefaultModel
	provider.SupportedModels = req.SupportedModels
	if req.ApiKey != "" {
		provider.ApiKey = req.ApiKey
	}

	provider, err = api.q.UpdateLLMProvider(r.Context(), provider)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, provider)
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
	p := db.LLMProvider{
		Name:            req.Name,
		BaseUrl:         req.BaseUrl,
		ApiKey:          req.ApiKey,
		ProviderType:    req.ProviderType,
		DefaultModel:    req.DefaultModel,
		SupportedModels: req.SupportedModels,
	}
	if err := api.db.Create(&p).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusCreated, p)
}

func (api *API) TestProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseUrl      string `json:"base_url"`
		ApiKey       string `json:"api_key"`
		Model        string `json:"model"`
		ProviderType string `json:"provider_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}

	url := strings.TrimSpace(req.BaseUrl)

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
			clientReq.Header.Set("x-api-key", req.ApiKey)
			clientReq.Header.Set("anthropic-version", "2023-06-01")
		} else {
			clientReq.Header.Set("Authorization", "Bearer "+req.ApiKey)
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

	openAiUrls = append(openAiUrls, url)
	anthropicUrls = append(anthropicUrls, url)

	cleanUrl := strings.TrimSuffix(strings.TrimSpace(url), "/")
	if !strings.HasSuffix(cleanUrl, "/chat/completions") && !strings.HasSuffix(cleanUrl, "/messages") {
		baseClean := cleanUrl
		if !strings.HasSuffix(baseClean, "/v1") {
			baseClean += "/v1"
		}
		openAiUrls = append(openAiUrls, baseClean+"/chat/completions")
		anthropicUrls = append(anthropicUrls, baseClean+"/messages")
	}

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
