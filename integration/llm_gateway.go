package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"agent-orchestrator/pkg/utils"

	"agent-orchestrator/db"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

type LLMGateway struct {
	q *db.Queries
}

func NewLLMGateway(database *gorm.DB) *LLMGateway {
	return &LLMGateway{
		q: db.New(database),
	}
}

func (g *LLMGateway) Mount(r chi.Router) {
	r.Post("/v1/chat/completions", g.proxyChatCompletions)
	r.Route("/proxy/agent/{agent_id}", func(r chi.Router) {
		r.Post("/v1/chat/completions", g.proxyChatCompletionsForAgent)
		r.Get("/v1/models", g.getModelsForAgent)
	})
}

func (g *LLMGateway) proxyChatCompletions(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	providerIDStr := r.Header.Get("X-Provider-ID")
	if providerIDStr == "" {
		http.Error(w, "X-Provider-ID header missing", http.StatusBadRequest)
		return
	}

	var providerID int
	fmt.Sscanf(providerIDStr, "%d", &providerID)

	provider, err := g.q.GetLLMProvider(r.Context(), int32(providerID))
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
		return
	}

	proxyReq, err := http.NewRequest(r.Method, utils.BuildProviderURL(provider.BaseUrl, "/chat/completions"), bytes.NewBuffer(bodyBytes))
	if err != nil {
		http.Error(w, "Failed to create proxy request", http.StatusInternalServerError)
		return
	}

	for k, vv := range r.Header {
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}
	proxyReq.Header.Set("Authorization", "Bearer "+provider.ApiKey)

	client := &http.Client{}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Failed to contact provider", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (g *LLMGateway) proxyChatCompletionsForAgent(w http.ResponseWriter, r *http.Request) {
	agentIDStr := chi.URLParam(r, "agent_id")
	agentID, err := strconv.Atoi(agentIDStr)
	if err != nil {
		http.Error(w, "Invalid agent ID", http.StatusBadRequest)
		return
	}

	agent, err := g.q.GetAgent(r.Context(), int32(agentID))
	if err != nil || agent.ProviderID == nil {
		http.Error(w, "Agent or provider not found", http.StatusNotFound)
		return
	}

	provider, err := g.q.GetLLMProvider(r.Context(), *agent.ProviderID)
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	var reqPayload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	json.Unmarshal(bodyBytes, &reqPayload)

	proxyReq, err := http.NewRequest(r.Method, utils.BuildProviderURL(provider.BaseUrl, "/chat/completions"), bytes.NewBuffer(bodyBytes))
	if err != nil {
		http.Error(w, "Failed to create proxy request", http.StatusInternalServerError)
		return
	}

	for k, vv := range r.Header {
		if strings.ToLower(k) == "authorization" {
			continue
		}
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}
	proxyReq.Header.Set("Authorization", "Bearer "+provider.ApiKey)

	client := &http.Client{}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Failed to contact provider", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if !reqPayload.Stream {
		respBodyBytes, _ := io.ReadAll(resp.Body)
		var resPayload struct {
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		json.Unmarshal(respBodyBytes, &resPayload)

		g.q.CreateProxyRequestLog(r.Context(), db.ProxyRequestLog{
			AgentID:          int32(agentID),
			ProviderID:       provider.ID,
			Model:            reqPayload.Model,
			PromptTokens:     resPayload.Usage.PromptTokens,
			CompletionTokens: resPayload.Usage.CompletionTokens,
			TotalTokens:      resPayload.Usage.TotalTokens,
		})

		w.Write(respBodyBytes)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()

		if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
			data := strings.TrimPrefix(line, "data: ")
			var chunk struct {
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err == nil && chunk.Usage.TotalTokens > 0 {
				g.q.CreateProxyRequestLog(r.Context(), db.ProxyRequestLog{
					AgentID:          int32(agentID),
					ProviderID:       provider.ID,
					Model:            reqPayload.Model,
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					TotalTokens:      chunk.Usage.TotalTokens,
				})
			}
		}
	}
}

func (g *LLMGateway) getModelsForAgent(w http.ResponseWriter, r *http.Request) {
	agentIDStr := chi.URLParam(r, "agent_id")
	agentID, err := strconv.Atoi(agentIDStr)
	if err != nil {
		http.Error(w, "Invalid agent ID", http.StatusBadRequest)
		return
	}

	agent, err := g.q.GetAgent(r.Context(), int32(agentID))
	if err != nil || agent.ProviderID == nil {
		http.Error(w, "Agent or provider not found", http.StatusNotFound)
		return
	}

	provider, err := g.q.GetLLMProvider(r.Context(), *agent.ProviderID)
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
		return
	}

	proxyReq, err := http.NewRequest(r.Method, utils.BuildProviderURL(provider.BaseUrl, "/models"), nil)
	if err != nil {
		http.Error(w, "Failed to create proxy request", http.StatusInternalServerError)
		return
	}

	for k, vv := range r.Header {
		if strings.ToLower(k) == "authorization" {
			continue
		}
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}
	proxyReq.Header.Set("Authorization", "Bearer "+provider.ApiKey)

	client := &http.Client{}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Failed to contact provider", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
