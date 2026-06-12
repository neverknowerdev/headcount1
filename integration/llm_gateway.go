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
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/tokens"
	"agent-orchestrator/pkg/utils"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

type LLMGateway struct {
	q        *db.Queries
	basePath string
	hub      interface{ BroadcastEvent(string, interface{}) }
}

func NewLLMGateway(database *gorm.DB) *LLMGateway {
	return &LLMGateway{
		q:        db.New(database),
		basePath: db.PaperclipHome(),
	}
}

func NewLLMGatewayWithHub(database *gorm.DB, hub interface{ BroadcastEvent(string, interface{}) }) *LLMGateway {
	return &LLMGateway{
		q:        db.New(database),
		basePath: db.PaperclipHome(),
		hub:      hub,
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

	// Initialize logger if we have a run ID
	var proxyLogger *logging.ProxyLogger
	var reqPayload struct {
		Model    string                   `json:"model"`
		Stream   bool                     `json:"stream"`
		Messages []map[string]interface{} `json:"messages"`
	}
	json.Unmarshal(bodyBytes, &reqPayload)

	if runIDStr := r.Header.Get("X-Run-ID"); runIDStr != "" {
		var runID int
		fmt.Sscanf(runIDStr, "%d", &runID)
		if runID > 0 {
			run, _, err := g.q.GetRunWithTask(r.Context(), int32(runID))
			if err == nil && run.Task.Company.ID > 0 {
				var loggerErr error
			proxyLogger, loggerErr = logging.NewProxyLoggerWithHub(
				g.basePath,
				run.Task.Company.ShortName,
				run.TaskID,
				run.ID,
				g.hub,
				g.q,
			)
				if loggerErr != nil {
					log.Printf("Warning: failed to create proxy logger: %v", loggerErr)
				} else {
					defer proxyLogger.Close()
					proxyLogger.LogRequest(reqPayload.Model, "opencode", provider.Name, bodyBytes)
					// Save log file path on the run
					g.q.UpdateRunLogFilePath(r.Context(), int32(runID), proxyLogger.FilePath())
					// Extract tool results from the request body. OpenAI's
					// chat-completions dialect represents them as
					// {role: "tool", content: "...", tool_call_id: "..."}
					// messages. The AI SDK includes these in every
					// subsequent LLM call after a tool is executed, so
					// the proxy is the only place that sees both the
					// tool call (in a prior response) and its result
					// (in the next request). We log each tool result as
					// a tool_response entry paired by tool_call_id with
					// the engine's prior tool_call entry.
					proxyLogger.LogToolResultsFromRequest(reqPayload.Model, provider.Name, reqPayload.Messages)
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
		if proxyLogger != nil {
			proxyLogger.LogError(reqPayload.Model, "opencode", provider.Name, err)
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
		// Non-streaming: buffer the response so we can log it and still forward to the client
		respBodyBytes, _ := io.ReadAll(resp.Body)

		// Extract token usage and per-message reasoning from response.
		// Reasoning in non-streaming responses can come from two places:
		//   1) Anthropic-style message.reasoning_content (or message.reasoning)
		//   2) OpenAI o-series completion_tokens_details.reasoning_tokens
		var resPayload struct {
			Choices []struct {
				Message struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
				CompletionTokensDetails struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		json.Unmarshal(respBodyBytes, &resPayload)

		usage := normalizedUsage{
			PromptTokens:     resPayload.Usage.PromptTokens,
			CompletionTokens: resPayload.Usage.CompletionTokens,
			TotalTokens:      resPayload.Usage.TotalTokens,
		}
		var nonStreamReasoning string
		for _, c := range resPayload.Choices {
			if c.Message.ReasoningContent != "" {
				nonStreamReasoning += c.Message.ReasoningContent
			} else if c.Message.Reasoning != "" {
				nonStreamReasoning += c.Message.Reasoning
			}
		}
		usage.ReasoningTokens = resolveReasoningTokens(
			resPayload.Usage.CompletionTokensDetails.ReasoningTokens,
			nonStreamReasoning,
		)

		// Save token stats to database
		if runIDStr := r.Header.Get("X-Run-ID"); runIDStr != "" {
			var runID int
			fmt.Sscanf(runIDStr, "%d", &runID)
			if runID > 0 {
				run, _, err := g.q.GetRunWithTask(r.Context(), int32(runID))
				if err == nil && run.Task.AgentID != nil {
					g.q.CreateProxyRequestLog(r.Context(), db.ProxyRequestLog{
						AgentID:          *run.Task.AgentID,
						ProviderID:       provider.ID,
						Model:            reqPayload.Model,
						PromptTokens:     usage.PromptTokens,
						CompletionTokens: usage.CompletionTokens,
						TotalTokens:      usage.TotalTokens,
					})
				}
				g.q.AddRunTokenStats(r.Context(), int32(runID), db.RunTokenStats{
					PromptTokens:     usage.PromptTokens,
					CompletionTokens: usage.CompletionTokens,
					ReasoningTokens:  usage.ReasoningTokens,
				})
			}
		}

		if proxyLogger != nil {
			proxyLogger.LogResponse(
				reqPayload.Model,
				provider.Name,
				resp.StatusCode,
				respBodyBytes,
				nonStreamReasoning,
				usage,
			)
		}

		w.Write(respBodyBytes)
		return
	}

	// Streaming path: parse SSE chunks line-by-line
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBody, streamErr := proxySSEStream(w, flusher, resp.Body, proxyLogger, reqPayload.Model, "opencode", provider.Name)
	if streamErr != nil {
		http.Error(w, streamErr.Error(), http.StatusGatewayTimeout)
		return
	}

	// Save token stats from streaming response
	if lastUsage != nil {
		if runIDStr := r.Header.Get("X-Run-ID"); runIDStr != "" {
			var runID int
			fmt.Sscanf(runIDStr, "%d", &runID)
			if runID > 0 {
				run, _, err := g.q.GetRunWithTask(r.Context(), int32(runID))
				if err == nil && run.Task.AgentID != nil {
					g.q.CreateProxyRequestLog(r.Context(), db.ProxyRequestLog{
						AgentID:          *run.Task.AgentID,
						ProviderID:       provider.ID,
						Model:            reqPayload.Model,
						PromptTokens:     lastUsage.PromptTokens,
						CompletionTokens: lastUsage.CompletionTokens,
						TotalTokens:      lastUsage.TotalTokens,
					})
				}
				g.q.AddRunTokenStats(r.Context(), int32(runID), db.RunTokenStats{
					PromptTokens:     lastUsage.PromptTokens,
					CompletionTokens: lastUsage.CompletionTokens,
					ReasoningTokens:  lastUsage.ReasoningTokens,
					ToolInputTokens:  lastUsage.ToolInputTokens,
				})
			}
		}
	}

	if proxyLogger != nil {
		var usage normalizedUsage
		if lastUsage != nil {
			usage = *lastUsage
		}
		proxyLogger.LogStreamResponse(reqPayload.Model, provider.Name, fullContent, fullReasoning, collectedToolCalls, rawBody, usage)
	}
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
		Model    string                   `json:"model"`
		Stream   bool                     `json:"stream"`
		Messages []map[string]interface{} `json:"messages"`
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
			proxyLogger, loggerErr = logging.NewProxyLoggerWithHub(
				g.basePath,
				run.Task.Company.ShortName,
				run.TaskID,
				run.ID,
				g.hub,
				g.q,
			)
				if loggerErr != nil {
					log.Printf("Warning: failed to create proxy logger: %v", loggerErr)
				} else {
					defer proxyLogger.Close()
					proxyLogger.LogRequest(reqPayload.Model, agent.Name, provider.Name, bodyBytes)
					// Save log file path on the run
					g.q.UpdateRunLogFilePath(r.Context(), int32(runID), proxyLogger.FilePath())
					proxyLogger.LogToolResultsFromRequest(reqPayload.Model, provider.Name, reqPayload.Messages)
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
			Choices []struct {
				Message struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
				CompletionTokensDetails struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		json.Unmarshal(respBodyBytes, &resPayload)

		usage := normalizedUsage{
			PromptTokens:     resPayload.Usage.PromptTokens,
			CompletionTokens: resPayload.Usage.CompletionTokens,
			TotalTokens:      resPayload.Usage.TotalTokens,
		}
		var nonStreamReasoning string
		for _, c := range resPayload.Choices {
			if c.Message.ReasoningContent != "" {
				nonStreamReasoning += c.Message.ReasoningContent
			} else if c.Message.Reasoning != "" {
				nonStreamReasoning += c.Message.Reasoning
			}
		}
		usage.ReasoningTokens = resolveReasoningTokens(
			resPayload.Usage.CompletionTokensDetails.ReasoningTokens,
			nonStreamReasoning,
		)

		runIDForStats := int32(0)
		if runIDStr := r.Header.Get("X-Run-ID"); runIDStr != "" {
			var runID int
			fmt.Sscanf(runIDStr, "%d", &runID)
			if runID > 0 {
				runIDForStats = int32(runID)
			}
		}

		g.q.CreateProxyRequestLog(r.Context(), db.ProxyRequestLog{
			AgentID:          int32(agentID),
			ProviderID:       provider.ID,
			Model:            reqPayload.Model,
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			TotalTokens:      usage.TotalTokens,
		})

		if runIDForStats > 0 {
			g.q.AddRunTokenStats(r.Context(), runIDForStats, db.RunTokenStats{
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				ReasoningTokens:  usage.ReasoningTokens,
			})
		}

		if proxyLogger != nil {
			proxyLogger.LogResponse(
				reqPayload.Model,
				provider.Name,
				resp.StatusCode,
				respBodyBytes,
				nonStreamReasoning,
				usage,
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

	fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBody, streamErr := proxySSEStream(w, flusher, resp.Body, proxyLogger, reqPayload.Model, agent.Name, provider.Name)
	if streamErr != nil {
		http.Error(w, streamErr.Error(), http.StatusGatewayTimeout)
		return
	}

	runIDForStats := int32(0)
	if runIDStr := r.Header.Get("X-Run-ID"); runIDStr != "" {
		var runID int
		fmt.Sscanf(runIDStr, "%d", &runID)
		if runID > 0 {
			runIDForStats = int32(runID)
		}
	}

	if proxyLogger != nil && lastUsage != nil {
		proxyLogger.LogStreamResponse(
			reqPayload.Model,
			provider.Name,
			fullContent,
			fullReasoning,
			collectedToolCalls,
			rawBody,
			*lastUsage,
		)
	}

	// Save token stats from streaming response to database
	if lastUsage != nil {
		g.q.CreateProxyRequestLog(r.Context(), db.ProxyRequestLog{
			AgentID:          int32(agentID),
			ProviderID:       provider.ID,
			Model:            reqPayload.Model,
			PromptTokens:     lastUsage.PromptTokens,
			CompletionTokens: lastUsage.CompletionTokens,
			TotalTokens:      lastUsage.TotalTokens,
		})
		if runIDForStats > 0 {
			g.q.AddRunTokenStats(r.Context(), runIDForStats, db.RunTokenStats{
				PromptTokens:     lastUsage.PromptTokens,
				CompletionTokens: lastUsage.CompletionTokens,
				ReasoningTokens:  lastUsage.ReasoningTokens,
				ToolInputTokens:  lastUsage.ToolInputTokens,
			})
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

// normalizedUsage is the gateway-internal alias for the shared tokens.Usage
// type. It exists so the function signatures below read more clearly and
// so the call sites don't have to import the tokens package at every
// reference.
type normalizedUsage = tokens.Usage

// resolveReasoningTokens prefers the provider-reported reasoning token
// count (OpenAI o-series completion_tokens_details.reasoning_tokens). If
// the provider didn't report it, fall back to a chars/4 estimate of the
// accumulated reasoning text — flagged as estimated in the UI via the
// "~" prefix.
func resolveReasoningTokens(reported int, reasoningText string) int {
	if reported > 0 {
		return reported
	}
	return tokens.Estimate(reasoningText)
}

// proxySSEStream copies an LLM-provider SSE stream to the client line by line,
// parsing chunks to accumulate content/reasoning/usage/tool_calls. It also
// detects stalls: if no bytes arrive from the provider for streamStallTimeout,
// the stream is considered dead and an error is returned so the caller can
// fail the run quickly instead of waiting for the outer HTTP timeout.
func proxySSEStream(
	w http.ResponseWriter,
	flusher http.Flusher,
	body io.Reader,
	proxyLogger *logging.ProxyLogger,
	model, agentName, providerName string,
) (fullContent, fullReasoning string, lastUsage *normalizedUsage, toolCalls []map[string]interface{}, rawBody []byte, err error) {
	const streamStallTimeout = 30 * time.Second
	// Tee the raw SSE bytes so the log file can record the unmodified
	// provider response (useful for debugging; same shape as we already
	// do for non-streaming LogResponse).
	rawBuf := &bytes.Buffer{}
	body = io.TeeReader(body, rawBuf)
	lastByteAt := time.Now()
	stallCh := make(chan struct{})
	stallErr := make(chan error, 1)
	stallDuration := make(chan time.Duration, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stallCh:
				return
			case <-ticker.C:
				d := time.Since(lastByteAt)
				if d > streamStallTimeout {
					stallErr <- fmt.Errorf("LLM stream stalled: no data for %v", d)
					stallDuration <- d
					return
				}
			}
		}
	}()

	type chunkToolCall struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type chunkChoice struct {
		Index int `json:"index"`
		Delta struct {
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []chunkToolCall `json:"tool_calls"`
		} `json:"delta"`
		// Non-delta message (sometimes the first chunk includes the full message)
		Message struct {
			Content   string          `json:"content"`
			ToolCalls []chunkToolCall `json:"tool_calls"`
		} `json:"message"`
	}
	type chunkData struct {
		Choices []chunkChoice `json:"choices"`
		Usage   *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}

	// Collected tool calls keyed by index so we can reassemble streamed deltas.
	toolCallsByIndex := map[int]*chunkToolCall{}

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		lastByteAt = time.Now()
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()

		if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
			data := strings.TrimPrefix(line, "data: ")
			var cd chunkData
			if err := json.Unmarshal([]byte(data), &cd); err == nil {
				for _, c := range cd.Choices {
					fullContent += c.Delta.Content
					fullReasoning += c.Delta.ReasoningContent
					// Accumulate streamed tool call deltas
					for _, tc := range c.Delta.ToolCalls {
						idx := tc.Index
						if idx < 0 {
							idx = 0
						}
						existing, ok := toolCallsByIndex[idx]
						if !ok {
							existing = &chunkToolCall{}
							toolCallsByIndex[idx] = existing
						}
						if tc.ID != "" {
							existing.ID = tc.ID
						}
						if tc.Type != "" {
							existing.Type = tc.Type
						}
						if tc.Function.Name != "" {
							existing.Function.Name = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							existing.Function.Arguments += tc.Function.Arguments
						}
					}
					// Also handle non-delta message format (some providers send full tool_calls in one chunk)
					for _, tc := range c.Message.ToolCalls {
						idx := tc.Index
						if idx < 0 {
							idx = 0
						}
						existing, ok := toolCallsByIndex[idx]
						if !ok {
							existing = &chunkToolCall{}
							toolCallsByIndex[idx] = existing
						}
						if tc.ID != "" {
							existing.ID = tc.ID
						}
						if tc.Type != "" {
							existing.Type = tc.Type
						}
						if tc.Function.Name != "" {
							existing.Function.Name = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							existing.Function.Arguments = tc.Function.Arguments
						}
					}
					if c.Message.Content != "" && fullContent == "" {
						fullContent = c.Message.Content
					}
				}
				if cd.Usage != nil && cd.Usage.TotalTokens > 0 {
					lastUsage = &normalizedUsage{
						PromptTokens:     cd.Usage.PromptTokens,
						CompletionTokens: cd.Usage.CompletionTokens,
						TotalTokens:      cd.Usage.TotalTokens,
						ReasoningTokens:  resolveReasoningTokens(cd.Usage.CompletionTokensDetails.ReasoningTokens, fullReasoning),
					}
				}
			}
		}
	}

	// Convert accumulated tool calls to a stable slice
	var collectedToolCalls []map[string]interface{}
	if len(toolCallsByIndex) > 0 {
		indices := make([]int, 0, len(toolCallsByIndex))
		for idx := range toolCallsByIndex {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			tc := toolCallsByIndex[idx]
			args := map[string]interface{}{}
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			collectedToolCalls = append(collectedToolCalls, map[string]interface{}{
				"id":   tc.ID,
				"type": tc.Type,
				"name": tc.Function.Name,
				"arguments": args,
			})
		}
	}
	// Roll the per-tool-call argument sizes up into the usage block so the
	// per-row and run-level stats include them.
	if lastUsage != nil && len(collectedToolCalls) > 0 {
		for _, tc := range collectedToolCalls {
			argsJSON, _ := json.Marshal(tc["arguments"])
			lastUsage.ToolInputTokens += tokens.EstimateBytes(argsJSON)
		}
	}
	close(stallCh)

	if err := scanner.Err(); err != nil {
		select {
		case stalled := <-stallErr:
			var d time.Duration
			select {
			case d = <-stallDuration:
		default:
			d = 0
		}
		if proxyLogger != nil {
			proxyLogger.LogStall(model, agentName, providerName, d)
		}
		return fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBuf.Bytes(), stalled
	default:
		if proxyLogger != nil {
			proxyLogger.LogError(model, agentName, providerName, err)
		}
		return fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBuf.Bytes(), fmt.Errorf("stream read error: %w", err)
	}
}

	select {
	case stalled := <-stallErr:
		var d time.Duration
		select {
		case d = <-stallDuration:
		default:
			d = 0
		}
		if proxyLogger != nil {
			proxyLogger.LogStall(model, agentName, providerName, d)
		}
		return fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBuf.Bytes(), stalled
	default:
	}
	return fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBuf.Bytes(), nil
}
