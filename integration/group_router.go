package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/utils"
	"github.com/go-chi/chi/v5"
)

// Rate-limit cooldowns double per consecutive rate limit, capped so a model
// is always retried within the hour.
const (
	rateLimitBaseCooldown = 5 * time.Minute
	rateLimitMaxCooldown  = time.Hour
)

// memberHealth is the gateway's in-memory routing state for one
// provider+model pair. Durable stats live in db.ModelRequestStat; this only
// holds what routing needs between requests (cooldowns and streaks).
type memberHealth struct {
	rateLimitedUntil time.Time
	rateLimitStreak  int
	failStreak       int
	lastTokensPerSec float64
}

type groupHealthState struct {
	mu      sync.Mutex
	members map[string]*memberHealth // key: "providerID/model"
}

func newGroupHealthState() *groupHealthState {
	return &groupHealthState{members: map[string]*memberHealth{}}
}

func healthKey(providerID int32, model string) string {
	return fmt.Sprintf("%d/%s", providerID, model)
}

func (s *groupHealthState) get(providerID int32, model string) memberHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.members[healthKey(providerID, model)]; ok {
		return *h
	}
	return memberHealth{}
}

func (s *groupHealthState) update(providerID int32, model string, fn func(*memberHealth)) memberHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := healthKey(providerID, model)
	h, ok := s.members[key]
	if !ok {
		h = &memberHealth{}
		s.members[key] = h
	}
	fn(h)
	return *h
}

// outcome classifies one upstream attempt.
type outcome int

const (
	outcomeSuccess outcome = iota
	outcomeRateLimited
	outcomeFailure
)

// classifyResponse decides whether an upstream response succeeded, was rate
// limited, or failed. Rate limits are detected from the 429 status and from
// error bodies — some gateways (e.g. OpenRouter's free tier) report rate
// limits inside a 200 body.
func classifyResponse(statusCode int, body []byte) (outcome, string) {
	if statusCode == http.StatusTooManyRequests {
		return outcomeRateLimited, extractErrorMessage(body, "rate limit exceeded (429)")
	}
	if statusCode >= 400 {
		msg := extractErrorMessage(body, fmt.Sprintf("provider returned HTTP %d", statusCode))
		if looksRateLimited(msg) {
			return outcomeRateLimited, msg
		}
		return outcomeFailure, msg
	}
	// 2xx: check for an error object embedded in the body.
	var probe struct {
		Error *struct {
			Message string      `json:"message"`
			Code    interface{} `json:"code"`
		} `json:"error"`
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(body, &probe); err == nil && probe.Error != nil && len(probe.Choices) == 0 {
		msg := probe.Error.Message
		if msg == "" {
			msg = "provider returned an error body"
		}
		if fmt.Sprintf("%v", probe.Error.Code) == "429" || looksRateLimited(msg) {
			return outcomeRateLimited, msg
		}
		return outcomeFailure, msg
	}
	return outcomeSuccess, ""
}

func looksRateLimited(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "rate limit") || strings.Contains(m, "rate-limit") ||
		strings.Contains(m, "too many requests") || strings.Contains(m, "quota") ||
		strings.Contains(m, "overloaded")
}

func extractErrorMessage(body []byte, fallback string) string {
	var probe struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &probe) == nil && probe.Error.Message != "" {
		return probe.Error.Message
	}
	s := strings.TrimSpace(string(body))
	if s != "" && len(s) <= 300 && !strings.Contains(strings.ToLower(s), "<html") {
		return fallback + ": " + s
	}
	return fallback
}

// orderCandidates returns the group's members in routing order: free members
// before paid ones; within each tier, members not in a rate-limit cooldown
// first (by failure streak, then priority), then cooling members as a last
// resort (soonest-recovering first).
func (g *LLMGateway) orderCandidates(members []db.ModelGroupMember, now time.Time) []db.ModelGroupMember {
	type scored struct {
		m      db.ModelGroupMember
		h      memberHealth
		cooled bool
	}
	var list []scored
	for _, m := range members {
		h := g.groupHealth.get(m.ProviderID, m.Model)
		list = append(list, scored{m: m, h: h, cooled: now.Before(h.rateLimitedUntil)})
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.m.IsFree != b.m.IsFree {
			return a.m.IsFree
		}
		if a.cooled != b.cooled {
			return !a.cooled
		}
		if a.cooled && b.cooled {
			return a.h.rateLimitedUntil.Before(b.h.rateLimitedUntil)
		}
		if a.h.failStreak != b.h.failStreak {
			return a.h.failStreak < b.h.failStreak
		}
		if a.h.lastTokensPerSec != b.h.lastTokensPerSec {
			return a.h.lastTokensPerSec > b.h.lastTokensPerSec
		}
		return a.m.Priority < b.m.Priority
	})
	out := make([]db.ModelGroupMember, len(list))
	for i, s := range list {
		out[i] = s.m
	}
	return out
}

// recordStat persists one routing outcome asynchronously.
func (g *LLMGateway) recordStat(stat db.ModelRequestStat) {
	go func() {
		if _, err := g.q.CreateModelRequestStat(context.Background(), stat); err != nil {
			log.Printf("Warning: failed to record model request stat: %v", err)
		}
	}()
}

// proxyLogModeHeader selects how much run logging the group router does when
// an X-Run-ID is present. The native engine sets "switches-only" because its
// agent loop already logs requests/responses and token stats itself; the
// router then only contributes model_switch and exhaustion entries.
const proxyLogModeHeader = "X-Proxy-Log-Mode"

// sendProviderRequest builds and sends a request to a provider endpoint
// (e.g. "/chat/completions" or "/models"), forwarding the incoming request's
// headers (except the ones in skipHeaders, matched case-insensitively) and
// swapping in the provider's own bearer token. This is the single place that
// talks to an LLM provider — direct proxying and the model group router's
// per-attempt retries all go through it.
func sendProviderRequest(ctx context.Context, method string, provider db.LLMProvider, path string, bodyBytes []byte, srcHeader http.Header, skipHeaders map[string]bool) (*http.Response, error) {
	var body io.Reader
	if bodyBytes != nil {
		body = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, method, utils.BuildProviderURL(provider.BaseUrl, path), body)
	if err != nil {
		return nil, err
	}
	for k, vv := range srcHeader {
		if skipHeaders[strings.ToLower(k)] {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	if bodyBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+provider.ApiKey)
	return providerHTTPClient.Do(req)
}

var providerHTTPClient = &http.Client{}

// proxyChatCompletionsForGroup is the OpenAI-compatible entrypoint for a
// model group. It tries the group's members in health order, failing over on
// errors and rate limits, and records a ModelRequestStat row per attempt.
func (g *LLMGateway) proxyChatCompletionsForGroup(w http.ResponseWriter, r *http.Request) {
	groupKey := chi.URLParam(r, "group_key")
	group, err := g.q.GetModelGroupByKey(r.Context(), groupKey)
	if err != nil {
		http.Error(w, "Model group not found", http.StatusNotFound)
		return
	}
	g.serveGroupChatCompletions(w, r, group)
}

func (g *LLMGateway) serveGroupChatCompletions(w http.ResponseWriter, r *http.Request, group db.ModelGroup) {
	group.Members = db.ExpandModelGroupMembers(group.Members)
	if len(group.Members) == 0 {
		http.Error(w, "Model group has no members", http.StatusBadGateway)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	reqPayload := parseChatCompletionsRequest(bodyBytes)

	// Parse the body into a generic map once so each attempt can swap the
	// "model" field while preserving everything else.
	var bodyMap map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &bodyMap); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	runID := parseRunID(r)
	switchesOnly := r.Header.Get(proxyLogModeHeader) == "switches-only"
	var proxyLogger *logging.ProxyLogger
	if !switchesOnly {
		proxyLogger = g.loggerForRun(r.Context(), runID, reqPayload.Model, group.Name, group.Name, bodyBytes, reqPayload.Messages)
		if proxyLogger != nil {
			defer proxyLogger.Close()
		}
	}

	candidates := g.orderCandidates(group.Members, time.Now())
	var lastErrMsg string
	var lastStatus int
	skipHeaders := map[string]bool{
		"authorization":                     true,
		"x-run-id":                          true,
		"content-length":                    true,
		strings.ToLower(proxyLogModeHeader): true,
	}

	for i, member := range candidates {
		provider := member.Provider
		bodyMap["model"] = member.Model
		attemptBody, _ := json.Marshal(bodyMap)

		if i > 0 {
			prev := candidates[i-1]
			reason := truncateMsg(lastErrMsg, 160)
			msg := fmt.Sprintf("Switching model: %s @ %s failed (%s), trying %s @ %s",
				prev.Model, prev.Provider.Name, reason, member.Model, provider.Name)
			log.Printf("[model-group %s] %s", group.Slug, msg)
			if proxyLogger != nil {
				proxyLogger.LogModelSwitch(prev.Provider.Name, prev.Model, provider.Name, member.Model, reason)
			} else if runID > 0 {
				g.logRunEvent(runID, "model_switch",
					fmt.Sprintf("Model switch: %s @ %s → %s @ %s (%s)", prev.Model, prev.Provider.Name, member.Model, provider.Name, reason),
					map[string]interface{}{
						"from_provider": prev.Provider.Name,
						"from_model":    prev.Model,
						"to_provider":   provider.Name,
						"to_model":      member.Model,
						"reason":        reason,
					})
			}
		}

		start := time.Now()
		resp, err := sendProviderRequest(r.Context(), http.MethodPost, provider, "/chat/completions", attemptBody, r.Header, skipHeaders)
		if err != nil {
			lastErrMsg = err.Error()
			lastStatus = http.StatusBadGateway
			g.noteFailure(group.ID, member, 0, time.Since(start), err.Error())
			continue
		}

		if runID > 0 {
			go g.q.TouchRunLastMessageTime(context.Background(), int32(runID))
		}

		// Any non-2xx (and 2xx-with-error for non-streaming) is inspected
		// before a single byte reaches the client, so we can still fail over.
		if resp.StatusCode >= 400 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			g.handleAttemptError(group.ID, member, resp.StatusCode, time.Since(start), respBody, &lastErrMsg)
			lastStatus = resp.StatusCode
			continue
		}

		if !reqPayload.Stream {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			duration := time.Since(start)

			result, errMsg := classifyResponse(resp.StatusCode, respBody)
			if result != outcomeSuccess {
				g.recordOutcome(group.ID, member, result, resp.StatusCode, duration, 0, 0, errMsg)
				lastErrMsg = errMsg
				lastStatus = resp.StatusCode
				continue
			}

			usage, reasoning := parseNonStreamUsage(respBody)
			g.recordOutcome(group.ID, member, outcomeSuccess, resp.StatusCode, duration, usage.PromptTokens, usage.CompletionTokens, "")
			if !switchesOnly {
				g.finishRunAccounting(r.Context(), runID, member, usage)
			}
			if proxyLogger != nil {
				proxyLogger.LogResponse(member.Model, provider.Name, resp.StatusCode, respBody, reasoning, usage)
			}

			copyResponseHeaders(w, resp.Header)
			w.WriteHeader(resp.StatusCode)
			w.Write(respBody)
			return
		}

		// Streaming: once the SSE relay starts we can no longer fail over,
		// but a mid-stream error/stall is still recorded as a failure.
		flusher, ok := w.(http.Flusher)
		if !ok {
			resp.Body.Close()
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}
		copyResponseHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)

		fullContent, fullReasoning, lastUsage, collectedToolCalls, rawBody, streamErr := proxySSEStream(
			w, flusher, resp.Body, proxyLogger, member.Model, "model-group:"+group.Slug, provider.Name)
		resp.Body.Close()
		duration := time.Since(start)

		if streamErr != nil {
			g.recordOutcome(group.ID, member, outcomeFailure, resp.StatusCode, duration, 0, 0, streamErr.Error())
			http.Error(w, streamErr.Error(), http.StatusGatewayTimeout)
			return
		}

		var usage normalizedUsage
		if lastUsage != nil {
			usage = *lastUsage
		}
		g.recordOutcome(group.ID, member, outcomeSuccess, resp.StatusCode, duration, usage.PromptTokens, usage.CompletionTokens, "")
		if !switchesOnly {
			g.finishRunAccounting(r.Context(), runID, member, usage)
		}
		if proxyLogger != nil {
			proxyLogger.LogStreamResponse(member.Model, provider.Name, fullContent, fullReasoning, collectedToolCalls, rawBody, usage)
		}
		return
	}

	msg := fmt.Sprintf("All %d models in group %q failed; last error: %s", len(candidates), group.Name, lastErrMsg)
	if proxyLogger != nil {
		proxyLogger.LogErrorMsg(msg)
	} else if switchesOnly && runID > 0 {
		g.logRunEvent(runID, "error", msg, nil)
	}
	if lastStatus == 0 {
		lastStatus = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(lastStatus)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": msg,
			"type":    "model_group_exhausted",
		},
	})
}

// handleAttemptError classifies a >=400 response and updates health/stats.
func (g *LLMGateway) handleAttemptError(groupID int32, member db.ModelGroupMember, status int, duration time.Duration, body []byte, lastErrMsg *string) {
	result, errMsg := classifyResponse(status, body)
	if result == outcomeSuccess {
		result = outcomeFailure
	}
	g.recordOutcome(groupID, member, result, status, duration, 0, 0, errMsg)
	*lastErrMsg = errMsg
}

// noteFailure records a network-level failure (no HTTP response).
func (g *LLMGateway) noteFailure(groupID int32, member db.ModelGroupMember, status int, duration time.Duration, errMsg string) {
	g.recordOutcome(groupID, member, outcomeFailure, status, duration, 0, 0, errMsg)
}

// recordOutcome updates the in-memory health state and persists a stat row
// for one attempt.
func (g *LLMGateway) recordOutcome(groupID int32, member db.ModelGroupMember, result outcome, status int, duration time.Duration, promptTokens, completionTokens int, errMsg string) {
	stat := db.ModelRequestStat{
		GroupID:          &groupID,
		ProviderID:       member.ProviderID,
		Model:            member.Model,
		StatusCode:       status,
		DurationMs:       duration.Milliseconds(),
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		ErrorMessage:     truncateMsg(errMsg, 500),
	}
	switch result {
	case outcomeSuccess:
		stat.Success = true
		if duration > 0 && completionTokens > 0 {
			stat.TokensPerSec = float64(completionTokens) / duration.Seconds()
		}
		g.groupHealth.update(member.ProviderID, member.Model, func(h *memberHealth) {
			h.failStreak = 0
			h.rateLimitStreak = 0
			h.rateLimitedUntil = time.Time{}
			if stat.TokensPerSec > 0 {
				h.lastTokensPerSec = stat.TokensPerSec
			}
		})
	case outcomeRateLimited:
		stat.RateLimited = true
		h := g.groupHealth.update(member.ProviderID, member.Model, func(h *memberHealth) {
			h.rateLimitStreak++
			cooldown := rateLimitBaseCooldown << uint(h.rateLimitStreak-1)
			if cooldown > rateLimitMaxCooldown || cooldown <= 0 {
				cooldown = rateLimitMaxCooldown
			}
			h.rateLimitedUntil = time.Now().Add(cooldown)
		})
		until := h.rateLimitedUntil
		stat.CooldownUntil = &until
	case outcomeFailure:
		g.groupHealth.update(member.ProviderID, member.Model, func(h *memberHealth) {
			h.failStreak++
		})
	}
	g.recordStat(stat)
}

// finishRunAccounting rolls token usage into the run aggregates when the
// request carried an X-Run-ID.
func (g *LLMGateway) finishRunAccounting(ctx context.Context, runID int, member db.ModelGroupMember, usage normalizedUsage) {
	if runID <= 0 {
		return
	}
	g.q.AddRunTokenStats(ctx, int32(runID), db.RunTokenStats{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		ToolInputTokens:  usage.ToolInputTokens,
		CachedTokens:     usage.CachedTokens,
	})
	if run, _, err := g.q.GetRunWithTask(ctx, int32(runID)); err == nil && run.Task.AgentID != nil {
		g.q.CreateProxyRequestLog(ctx, db.ProxyRequestLog{
			AgentID:          *run.Task.AgentID,
			ProviderID:       member.ProviderID,
			Model:            member.Model,
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			TotalTokens:      usage.TotalTokens,
		})
	}
}

// loggerForRun builds a ProxyLogger when an X-Run-ID header is present,
// mirroring the behavior of the other proxy entrypoints.
func (g *LLMGateway) loggerForRun(ctx context.Context, runID int, model, sourceName, providerName string, bodyBytes []byte, messages []map[string]interface{}) *logging.ProxyLogger {
	if runID <= 0 {
		return nil
	}
	run, _, err := g.q.GetRunWithTask(ctx, int32(runID))
	if err != nil || run.Task.Company.ID == 0 {
		return nil
	}
	proxyLogger, loggerErr := logging.NewProxyLoggerWithHub(
		g.basePath,
		run.Task.Company.ShortName,
		run.TaskID,
		run.ID,
		g.hub,
		g.q,
	)
	if loggerErr != nil {
		log.Printf("Warning: failed to create proxy logger: %v", loggerErr)
		return nil
	}
	proxyLogger.LogRequest(model, sourceName, providerName, bodyBytes)
	g.q.UpdateRunLogFilePath(ctx, int32(runID), proxyLogger.FilePath())
	proxyLogger.LogToolResultsFromRequest(model, providerName, messages)
	return proxyLogger
}

// logRunEvent appends a structured entry to a run's log and broadcasts it
// over the WebSocket hub. Used in switches-only log mode, where no
// ProxyLogger (and thus no log file) is created — the engine's session
// logger owns the file; the router only contributes routing events.
func (g *LLMGateway) logRunEvent(runID int, entryType, content string, extra map[string]interface{}) {
	entry := map[string]interface{}{
		"type":    entryType,
		"content": content,
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
		"seq":     logging.NextRunLogSeq(context.Background(), g.q, int32(runID)),
	}
	for k, v := range extra {
		entry[k] = v
	}
	if g.hub != nil {
		g.hub.BroadcastEvent("run_log", map[string]interface{}{
			"run_id": int32(runID),
			"entry":  entry,
		})
	}
	go func() {
		for i := 0; i < 3; i++ {
			if err := g.q.AppendRunLogEntry(context.Background(), int32(runID), entry); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

func parseRunID(r *http.Request) int {
	runIDStr := r.Header.Get("X-Run-ID")
	if runIDStr == "" {
		return 0
	}
	var runID int
	fmt.Sscanf(runIDStr, "%d", &runID)
	return runID
}

func copyResponseHeaders(w http.ResponseWriter, h http.Header) {
	for k, vv := range h {
		if strings.ToLower(k) == "content-length" {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
}

func truncateMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// parseNonStreamUsage extracts normalized token usage and reasoning text
// from a buffered (non-streaming) chat-completions response body.
func parseNonStreamUsage(respBody []byte) (normalizedUsage, string) {
	var resPayload struct {
		Choices []struct {
			Message struct {
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
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
	json.Unmarshal(respBody, &resPayload)

	var reasoning string
	for _, c := range resPayload.Choices {
		if c.Message.ReasoningContent != "" {
			reasoning += c.Message.ReasoningContent
		} else if c.Message.Reasoning != "" {
			reasoning += c.Message.Reasoning
		}
	}
	usage := normalizedUsage{
		PromptTokens:     resPayload.Usage.PromptTokens,
		CompletionTokens: resPayload.Usage.CompletionTokens,
		TotalTokens:      resPayload.Usage.TotalTokens,
		CachedTokens:     resPayload.Usage.PromptTokensDetails.CachedTokens,
	}
	usage.ReasoningTokens = resolveReasoningTokens(resPayload.Usage.CompletionTokensDetails.ReasoningTokens, reasoning)
	return usage, reasoning
}

// getModelsForGroup answers /v1/models for a group endpoint. The group's
// slug is listed as a routable pseudo-model (any requested model is
// overridden by the router anyway), followed by the concrete members.
func (g *LLMGateway) getModelsForGroup(w http.ResponseWriter, r *http.Request) {
	groupKey := chi.URLParam(r, "group_key")
	group, err := g.q.GetModelGroupByKey(r.Context(), groupKey)
	if err != nil {
		http.Error(w, "Model group not found", http.StatusNotFound)
		return
	}
	g.serveGroupModels(w, group)
}

func (g *LLMGateway) serveGroupModels(w http.ResponseWriter, group db.ModelGroup) {
	group.Members = db.ExpandModelGroupMembers(group.Members)
	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	data := []modelEntry{{ID: group.Slug, Object: "model", OwnedBy: "model-group"}}
	for _, m := range group.Members {
		data = append(data, modelEntry{ID: m.Model, Object: "model", OwnedBy: m.Provider.Name})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": data})
}
