package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/utils"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

type LLMGateway struct {
	q        *db.Queries
	basePath string
}

func NewLLMGateway(database *gorm.DB) *LLMGateway {
	return &LLMGateway{
		q:        db.New(database),
		basePath: db.PaperclipHome(),
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
	var providerID int
	if providerIDStr != "" {
		fmt.Sscanf(providerIDStr, "%d", &providerID)
	}

	// Resolve provider: use X-Provider-ID header if present, otherwise
	// require it (the header is set by opencode config model headers).
	if providerID == 0 {
		http.Error(w, "X-Provider-ID header missing", http.StatusBadRequest)
		return
	}

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
		lk := strings.ToLower(k)
		if lk == "x-provider-id" || lk == "x-run-id" {
			continue // don't forward internal headers to provider
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

	// Heartbeat: touch the run's last_message_time so stale-run
	// detection knows the LLM is still working.
	if runIDStr := r.Header.Get("X-Run-ID"); runIDStr != "" {
		var runID int
		fmt.Sscanf(runIDStr, "%d", &runID)
		if runID > 0 {
			go g.q.TouchRunLastMessageTime(context.Background(), int32(runID))
		}
	}

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

	// Initialize logger if we have a run ID
	var proxyLogger *logging.ProxyLogger
	if runIDStr := r.Header.Get("X-Run-ID"); runIDStr != "" {
		var runID int
		fmt.Sscanf(runIDStr, "%d", &runID)
		if runID > 0 {
			run, _, err := g.q.GetRunWithTask(r.Context(), int32(runID))
			if err == nil && run.Task.Company.ID > 0 {
				var loggerErr error
				proxyLogger, loggerErr = logging.NewProxyLogger(
					g.basePath,
					run.Task.Company.ShortName,
					run.TaskID,
					run.ID,
				)
				if loggerErr != nil {
					log.Printf("Warning: failed to create proxy logger: %v", loggerErr)
				} else {
					defer proxyLogger.Close()
					proxyLogger.LogRequest(reqPayload.Model, agent.Name, provider.Name, bodyBytes)
				}
			}
		}
	}

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
		if proxyLogger != nil {
			proxyLogger.LogError(reqPayload.Model, agent.Name, provider.Name, err)
		}
		http.Error(w, "Failed to contact provider", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Heartbeat: touch the run's last_message_time so stale-run
	// detection knows the LLM is still working.
	if runIDStr := r.Header.Get("X-Run-ID"); runIDStr != "" {
		var runID int
		fmt.Sscanf(runIDStr, "%d", &runID)
		if runID > 0 {
			go g.q.TouchRunLastMessageTime(context.Background(), int32(runID))
		}
	}

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

		if proxyLogger != nil {
			proxyLogger.LogResponse(
				reqPayload.Model,
				provider.Name,
				resp.StatusCode,
				respBodyBytes,
				resPayload.Usage.PromptTokens,
				resPayload.Usage.CompletionTokens,
				resPayload.Usage.TotalTokens,
			)
		}

		w.Write(respBodyBytes)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	var streamChunks [][]byte
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

				if proxyLogger != nil {
					proxyLogger.LogStreamResponse(
						reqPayload.Model,
						provider.Name,
						streamChunks,
						chunk.Usage.PromptTokens,
						chunk.Usage.CompletionTokens,
						chunk.Usage.TotalTokens,
					)
				}
			}
			streamChunks = append(streamChunks, []byte(data))
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
