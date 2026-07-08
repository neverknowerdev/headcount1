package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/tokens"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

type LLMGateway struct {
	q           *db.Queries
	basePath    string
	hub         interface{ BroadcastEvent(string, interface{}) }
	groupHealth *groupHealthState
}

func NewLLMGateway(database *gorm.DB) *LLMGateway {
	return NewLLMGatewayWithHub(database, nil)
}

func NewLLMGatewayWithHub(database *gorm.DB, hub interface{ BroadcastEvent(string, interface{}) }) *LLMGateway {
	return &LLMGateway{
		q:           db.New(database),
		basePath:    db.PaperclipHome(),
		hub:         hub,
		groupHealth: newGroupHealthState(),
	}
}

func (g *LLMGateway) Mount(r chi.Router) {
	r.Post("/v1/chat/completions", g.proxyChatCompletionsForProvider)
	r.Route("/proxy/agent/{agent_id}", func(r chi.Router) {
		r.Post("/v1/chat/completions", g.proxyChatCompletionsForAgent)
		r.Get("/v1/models", g.getModelsForAgent)
	})
	r.Route("/proxy/group/{group_key}", func(r chi.Router) {
		r.Post("/v1/chat/completions", g.proxyChatCompletionsForGroup)
		r.Get("/v1/models", g.getModelsForGroup)
	})
}

// chatCompletionsRequest is the subset of an OpenAI chat-completions body the
// gateway inspects: the model, whether the client wants a stream, and the
// messages (used for tool-result logging).
type chatCompletionsRequest struct {
	Model    string                   `json:"model"`
	Stream   bool                     `json:"stream"`
	Messages []map[string]interface{} `json:"messages"`
}

func parseChatCompletionsRequest(body []byte) chatCompletionsRequest {
	var req chatCompletionsRequest
	json.Unmarshal(body, &req)
	return req
}

// directProxyRequest carries everything the shared relay pipeline needs. The
// generic and per-agent entrypoints differ only in how they resolve these
// fields (provider selection and stat attribution); the send/relay/record
// work itself is identical and lives in relayProviderResponse.
type directProxyRequest struct {
	provider    db.LLMProvider
	sourceName  string // log label: "llm-proxy" or the agent's name
	model       string
	stream      bool
	bodyBytes   []byte
	logger      *logging.ProxyLogger
	runID       int
	agentID     int32           // owner of the ProxyRequestLog rows; 0 to skip
	skipHeaders map[string]bool // request headers not forwarded upstream
}

// proxyChatCompletionsForProvider proxies to the provider named by the
// X-Provider-ID header. Stats are attributed to the agent that owns the
// request's run (if any).
func (g *LLMGateway) proxyChatCompletionsForProvider(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	providerID := 0
	if s := r.Header.Get("X-Provider-ID"); s != "" {
		fmt.Sscanf(s, "%d", &providerID)
	}
	if providerID == 0 {
		http.Error(w, "X-Provider-ID header missing", http.StatusBadRequest)
		return
	}
	provider, err := g.q.GetLLMProvider(r.Context(), int32(providerID))
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
		return
	}

	req := parseChatCompletionsRequest(bodyBytes)
	runID := parseRunID(r)
	logger := g.loggerForRun(r.Context(), runID, req.Model, "llm-proxy", provider.Name, bodyBytes, req.Messages)
	if logger != nil {
		defer logger.Close()
	}

	g.relayProviderResponse(w, r, directProxyRequest{
		provider:    provider,
		sourceName:  "llm-proxy",
		model:       req.Model,
		stream:      req.Stream,
		bodyBytes:   bodyBytes,
		logger:      logger,
		runID:       runID,
		agentID:     g.runAgentID(r.Context(), runID),
		skipHeaders: map[string]bool{"x-provider-id": true, "x-run-id": true},
	})
}

// agentProxyTarget is where an agent's LLM traffic should go: a model group
// (routed with failover by the group router) or the agent's fixed provider.
// Exactly one of group/provider is set.
type agentProxyTarget struct {
	agent    db.Agent
	group    *db.ModelGroup
	provider db.LLMProvider
}

// resolveAgentProxyTarget loads the agent addressed by the request's
// {agent_id} URL param and resolves its model group or provider. On failure
// it writes the HTTP error itself and returns ok=false.
func (g *LLMGateway) resolveAgentProxyTarget(w http.ResponseWriter, r *http.Request) (agentProxyTarget, bool) {
	agentID, err := strconv.Atoi(chi.URLParam(r, "agent_id"))
	if err != nil {
		http.Error(w, "Invalid agent ID", http.StatusBadRequest)
		return agentProxyTarget{}, false
	}

	agent, err := g.q.GetAgent(r.Context(), int32(agentID))
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return agentProxyTarget{}, false
	}

	// Agents bound to a model group route through the group router
	// (free-first ordering, failover, per-attempt stats).
	if agent.ModelGroupID != nil {
		group, gErr := g.q.GetModelGroup(r.Context(), *agent.ModelGroupID)
		if gErr != nil {
			http.Error(w, "Model group not found", http.StatusNotFound)
			return agentProxyTarget{}, false
		}
		return agentProxyTarget{agent: agent, group: &group}, true
	}

	if agent.ProviderID == nil {
		http.Error(w, "Agent has no provider or model group configured", http.StatusNotFound)
		return agentProxyTarget{}, false
	}
	provider, err := g.q.GetLLMProvider(r.Context(), *agent.ProviderID)
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
		return agentProxyTarget{}, false
	}
	return agentProxyTarget{agent: agent, provider: provider}, true
}

// proxyChatCompletionsForAgent proxies on behalf of a specific agent. Agents
// bound to a model group hand off to the group router; otherwise the agent's
// own provider is used and stats are attributed directly to the agent.
func (g *LLMGateway) proxyChatCompletionsForAgent(w http.ResponseWriter, r *http.Request) {
	target, ok := g.resolveAgentProxyTarget(w, r)
	if !ok {
		return
	}
	if target.group != nil {
		g.serveGroupChatCompletions(w, r, *target.group)
		return
	}
	agent, provider := target.agent, target.provider

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	req := parseChatCompletionsRequest(bodyBytes)
	runID := parseRunID(r)
	logger := g.loggerForRun(r.Context(), runID, req.Model, agent.Name, provider.Name, bodyBytes, req.Messages)
	if logger != nil {
		defer logger.Close()
	}

	g.relayProviderResponse(w, r, directProxyRequest{
		provider:    provider,
		sourceName:  agent.Name,
		model:       req.Model,
		stream:      req.Stream,
		bodyBytes:   bodyBytes,
		logger:      logger,
		runID:       runID,
		agentID:     agent.ID,
		skipHeaders: map[string]bool{"authorization": true},
	})
}

// relayProviderResponse sends an already-resolved chat-completions request to
// its provider and relays the response back to the client — buffering and
// logging a non-streaming body, or piping SSE chunks for a streaming one —
// while recording token stats and proxy logs. It is the shared tail of the
// generic and per-agent entrypoints; only provider resolution and stat
// attribution differ between them, and those arrive on p.
func (g *LLMGateway) relayProviderResponse(w http.ResponseWriter, r *http.Request, p directProxyRequest) {
	resp, err := sendProviderRequest(r.Context(), r.Method, p.provider, "/chat/completions", p.bodyBytes, r.Header, p.skipHeaders)
	if err != nil {
		if p.logger != nil {
			p.logger.LogError(p.model, p.sourceName, p.provider.Name, err)
		}
		http.Error(w, "Failed to contact provider", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Heartbeat: touch the run's last_message_time so stale-run detection
	// knows the LLM is still working.
	if p.runID > 0 {
		go g.q.TouchRunLastMessageTime(context.Background(), int32(p.runID))
	}

	copyResponseHeaders(w, resp.Header)
	w.WriteHeader(resp.StatusCode)

	if !p.stream {
		respBodyBytes, _ := io.ReadAll(resp.Body)
		usage, reasoning := parseNonStreamUsage(respBodyBytes)
		g.recordProxyStats(r.Context(), p.provider, p.model, p.runID, p.agentID, usage)
		if p.logger != nil {
			p.logger.LogResponse(p.model, p.provider.Name, resp.StatusCode, respBodyBytes, reasoning, usage)
		}
		w.Write(respBodyBytes)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBody, streamErr := proxySSEStream(
		w, flusher, resp.Body, p.logger, p.model, p.sourceName, p.provider.Name)
	if streamErr != nil {
		http.Error(w, streamErr.Error(), http.StatusGatewayTimeout)
		return
	}
	if lastUsage != nil {
		g.recordProxyStats(r.Context(), p.provider, p.model, p.runID, p.agentID, *lastUsage)
	}
	if p.logger != nil {
		var usage normalizedUsage
		if lastUsage != nil {
			usage = *lastUsage
		}
		p.logger.LogStreamResponse(p.model, p.provider.Name, fullContent, fullReasoning, collectedToolCalls, rawBody, usage)
	}
}

// recordProxyStats persists usage for one completed direct-proxy request: a
// per-agent ProxyRequestLog row (when agentID > 0) and per-run token stats
// (when runID > 0). Model-group requests use finishRunAccounting instead,
// which attributes usage to the member that actually served the request.
func (g *LLMGateway) recordProxyStats(ctx context.Context, provider db.LLMProvider, model string, runID int, agentID int32, usage normalizedUsage) {
	if agentID > 0 {
		g.q.CreateProxyRequestLog(ctx, db.ProxyRequestLog{
			AgentID:          agentID,
			ProviderID:       provider.ID,
			Model:            model,
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			TotalTokens:      usage.TotalTokens,
		})
	}
	if runID > 0 {
		g.q.AddRunTokenStats(ctx, int32(runID), db.RunTokenStats{
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			ReasoningTokens:  usage.ReasoningTokens,
			ToolInputTokens:  usage.ToolInputTokens,
			CachedTokens:     usage.CachedTokens,
		})
	}
}

// runAgentID returns the agent that owns a run's task, or 0 when there is no
// run or the run isn't bound to an agent. Used to attribute a provider-named
// proxy request (which doesn't carry an agent) to the right agent.
func (g *LLMGateway) runAgentID(ctx context.Context, runID int) int32 {
	if runID <= 0 {
		return 0
	}
	run, _, err := g.q.GetRunWithTask(ctx, int32(runID))
	if err != nil || run.Task.AgentID == nil {
		return 0
	}
	return *run.Task.AgentID
}

// getModelsForAgent answers /v1/models on behalf of an agent: the group's
// pseudo-model list for group-bound agents, the provider's own list otherwise.
func (g *LLMGateway) getModelsForAgent(w http.ResponseWriter, r *http.Request) {
	target, ok := g.resolveAgentProxyTarget(w, r)
	if !ok {
		return
	}
	if target.group != nil {
		g.serveGroupModels(w, *target.group)
		return
	}

	resp, err := sendProviderRequest(r.Context(), r.Method, target.provider, "/models", nil, r.Header, map[string]bool{"authorization": true})
	if err != nil {
		http.Error(w, "Failed to contact provider", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w, resp.Header)
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
	// lastByteAt is written by the scanner loop and read by the watchdog
	// goroutine, so it must be atomic (stored as unix nanos).
	var lastByteAt atomic.Int64
	lastByteAt.Store(time.Now().UnixNano())
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
				d := time.Since(time.Unix(0, lastByteAt.Load()))
				if d > streamStallTimeout {
					stallErr <- fmt.Errorf("LLM stream stalled: no data for %v", d)
					stallDuration <- d
					return
				}
			}
		}
	}()

	// checkStall reports (and logs) a pending stall detected by the watchdog.
	checkStall := func() (error, bool) {
		select {
		case stalled := <-stallErr:
			var d time.Duration
			select {
			case d = <-stallDuration:
			default:
			}
			if proxyLogger != nil {
				proxyLogger.LogStall(model, agentName, providerName, d)
			}
			return stalled, true
		default:
			return nil, false
		}
	}

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
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}

	// Collected tool calls keyed by index so we can reassemble streamed deltas.
	toolCallsByIndex := map[int]*chunkToolCall{}
	// mergeToolCall folds one tool-call fragment into the accumulator.
	// Streamed deltas append to arguments; a non-delta message carries the
	// complete arguments in one chunk and replaces them.
	mergeToolCall := func(tc chunkToolCall, appendArgs bool) {
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
			if appendArgs {
				existing.Function.Arguments += tc.Function.Arguments
			} else {
				existing.Function.Arguments = tc.Function.Arguments
			}
		}
	}

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		lastByteAt.Store(time.Now().UnixNano())
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
					for _, tc := range c.Delta.ToolCalls {
						mergeToolCall(tc, true)
					}
					// Non-delta message format (some providers send full tool_calls in one chunk)
					for _, tc := range c.Message.ToolCalls {
						mergeToolCall(tc, false)
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
						CachedTokens:     cd.Usage.PromptTokensDetails.CachedTokens,
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
				"id":        tc.ID,
				"type":      tc.Type,
				"name":      tc.Function.Name,
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

	if stalled, ok := checkStall(); ok {
		return fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBuf.Bytes(), stalled
	}
	if err := scanner.Err(); err != nil {
		if proxyLogger != nil {
			proxyLogger.LogError(model, agentName, providerName, err)
		}
		return fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBuf.Bytes(), fmt.Errorf("stream read error: %w", err)
	}
	return fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBuf.Bytes(), nil
}
