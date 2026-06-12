package logging

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/tokens"
)

// Usage is re-exported from pkg/tokens so call sites in this package can
// name the type without importing the tokens package directly.
type Usage = tokens.Usage

type ProxyLogger struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	basePath string
	hub      interface{ BroadcastEvent(string, interface{}) }
	q        *db.Queries
	runID    int32
	// loggedToolResults dedupes tool result entries across LLM requests:
	// the same tool_call_id appears in every subsequent request's
	// messages[] (the conversation history keeps growing), but we only
	// want to log it once.
	loggedToolResults map[string]bool
}

func NewProxyLogger(basePath, companyShortName string, taskID int32, runID int32) (*ProxyLogger, error) {
	return NewProxyLoggerWithHub(basePath, companyShortName, taskID, runID, nil, nil)
}

func NewProxyLoggerWithHub(basePath, companyShortName string, taskID int32, runID int32, hub interface{ BroadcastEvent(string, interface{}) }, q *db.Queries) (*ProxyLogger, error) {
	logDir := filepath.Join(basePath, "data", companyShortName, "logs", fmt.Sprintf("%d", taskID))
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	logFile := filepath.Join(logDir, fmt.Sprintf("run-%d.log", runID))
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	return &ProxyLogger{
		file:              f,
		filePath:          logFile,
		basePath:          basePath,
		hub:               hub,
		q:                 q,
		runID:             runID,
		loggedToolResults: map[string]bool{},
	}, nil
}

func (l *ProxyLogger) FilePath() string {
	return l.filePath
}

func (l *ProxyLogger) broadcastLog(entryType, content string, extra map[string]interface{}) {
	if l.hub == nil || l.runID <= 0 {
		return
	}
	entry := map[string]interface{}{
		"type":    entryType,
		"content": content,
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	for k, v := range extra {
		entry[k] = v
	}
	l.hub.BroadcastEvent("run_log", map[string]interface{}{
		"run_id": l.runID,
		"entry":  entry,
	})
}

func (l *ProxyLogger) persistLog(entryType, content string, extra map[string]interface{}) {
	if l.q == nil || l.runID <= 0 {
		return
	}
	entry := map[string]interface{}{
		"type":    entryType,
		"content": content,
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	for k, v := range extra {
		entry[k] = v
	}
	go func() {
		for i := 0; i < 3; i++ {
			err := l.q.AppendRunLogEntry(context.Background(), l.runID, entry)
			if err == nil {
				break
			}
			fmt.Printf("AppendRunLogEntry error (attempt %d): %v\n", i+1, err)
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

func (l *ProxyLogger) LogRequest(model, agentName, providerName string, requestBody []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	l.file.WriteString(fmt.Sprintf("\n=== LLM Request [%s] ===\n", ts))
	l.file.WriteString(fmt.Sprintf("Model: %s\n", model))
	l.file.WriteString(fmt.Sprintf("Agent: %s\n", agentName))
	l.file.WriteString(fmt.Sprintf("Provider: %s\n", providerName))
	l.file.WriteString("---\n")
	l.file.Write(requestBody)
	l.file.WriteString("\n")

	l.broadcastLog("request", string(requestBody), map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
	})
	l.persistLog("request", string(requestBody), map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
	})
}

func (l *ProxyLogger) LogResponse(model, providerName string, statusCode int, responseBody []byte, reasoningContent string, usage Usage) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	l.file.WriteString(fmt.Sprintf("\n=== LLM Response [%s] ===\n", ts))
	l.file.WriteString(fmt.Sprintf("Model: %s\n", model))
	l.file.WriteString(fmt.Sprintf("Provider: %s\n", providerName))
	l.file.WriteString(fmt.Sprintf("Status: %d\n", statusCode))
	l.file.WriteString(fmt.Sprintf("Tokens: prompt=%d completion=%d total=%d reasoning=%d\n",
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.ReasoningTokens))
	l.file.WriteString("---\n")
	// Write the raw response body unmodified, same as LogRequest.
	// The reasoning field is also folded into respData below so the
	// frontend can still render the reasoning panel; we don't need to
	// duplicate it in the file.
	l.file.Write(responseBody)
	l.file.WriteString("\n")

	// Build a structured response payload. The shape matches what the
	// frontend's getAgentMessage already understands.
	respData := map[string]interface{}{}
	if len(responseBody) > 0 {
		// Try to forward the original LLM response body as the "raw" content;
		// the UI's getAgentMessage will then dispatch on shape.
		respData["raw"] = string(responseBody)
	}
	if reasoningContent != "" {
		respData["reasoning"] = reasoningContent
	}
	if usage.PromptTokens > 0 || usage.CompletionTokens > 0 || usage.TotalTokens > 0 {
		respData["tokens"] = map[string]int{
			"prompt":     usage.PromptTokens,
			"completion": usage.CompletionTokens,
			"total":      usage.TotalTokens,
			"reasoning":  usage.ReasoningTokens,
		}
	}
	respBytes, _ := json.Marshal(respData)

	l.broadcastLog("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"status_code": statusCode,
	})
	l.persistLog("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"status_code": statusCode,
	})
}

func (l *ProxyLogger) LogStreamResponse(model, providerName string, content, reasoningContent string, toolCalls []map[string]interface{}, rawBody []byte, usage Usage) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	l.file.WriteString(fmt.Sprintf("\n=== LLM Response [%s] ===\n", ts))
	l.file.WriteString(fmt.Sprintf("Model: %s\n", model))
	l.file.WriteString(fmt.Sprintf("Provider: %s\n", providerName))
	l.file.WriteString(fmt.Sprintf("Tokens: prompt=%d completion=%d total=%d reasoning=%d tool_in=%d\n",
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.ReasoningTokens, usage.ToolInputTokens))
	l.file.WriteString("---\n")
	// Write the raw SSE response body unmodified, the same way
	// LogRequest writes the raw request body. This makes the log file
	// a faithful replay of the wire traffic and avoids reformatting
	// the provider's JSON.
	if len(rawBody) > 0 {
		l.file.Write(rawBody)
	} else {
		// Fallback: no raw body was captured (e.g. very early failure
		// before the stream started). Synthesize a record from the
		// assembled fields so we still have something to inspect.
		if reasoningContent != "" {
			l.file.WriteString(fmt.Sprintf("[Reasoning]\n%s\n", reasoningContent))
		}
		if content != "" {
			l.file.WriteString(fmt.Sprintf("[Content]\n%s\n", content))
		}
		if len(toolCalls) > 0 {
			l.file.WriteString(fmt.Sprintf("[ToolCalls]\n%s\n", mustJSON(toolCalls)))
		}
		if reasoningContent == "" && content == "" && len(toolCalls) == 0 {
			l.file.WriteString("(no content)\n")
		}
	}
	l.file.WriteString("\n")

	// Build a structured response from the streaming result
	respData := map[string]interface{}{}
	if content != "" {
		respData["content"] = content
	}
	if reasoningContent != "" {
		respData["reasoning"] = reasoningContent
	}
	if len(toolCalls) > 0 {
		respData["tool_calls"] = toolCalls
	}
	if usage.PromptTokens > 0 || usage.CompletionTokens > 0 || usage.TotalTokens > 0 {
		respData["tokens"] = map[string]int{
			"prompt":     usage.PromptTokens,
			"completion": usage.CompletionTokens,
			"total":      usage.TotalTokens,
			"reasoning":  usage.ReasoningTokens,
		}
	}
	if len(rawBody) > 0 {
		// Forward the unmodified provider response so the frontend
		// can still inspect the original shape (e.g. for providers
		// that emit things we don't have explicit fields for).
		respData["raw"] = string(rawBody)
	}
	respBytes, _ := json.Marshal(respData)
	l.broadcastLog("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"status_code": 200,
	})
	l.persistLog("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"status_code": 200,
	})

	// Also emit a separate tool_call entry for each tool call so they
	// appear as their own rows in the log viewer (with their own icons
	// and per-tool token counts).
	for _, tc := range toolCalls {
		name, _ := tc["name"].(string)
		if name == "" {
			name = "unknown"
		}
		argsJSON, _ := json.Marshal(tc["arguments"])
		inTokens := tokens.EstimateBytes(argsJSON)
		l.broadcastLog("tool_call", string(argsJSON), map[string]interface{}{
			"tool_name":      name,
			"input_tokens":   inTokens,
			"output_tokens":  inTokens, // backwards-compat alias used by the UI today
		})
		l.persistLog("tool_call", string(argsJSON), map[string]interface{}{
			"tool_name":      name,
			"input_tokens":   inTokens,
			"output_tokens":  inTokens,
		})
	}

	// Inject the actual prompt_tokens into the engine's "request" entry. The
	// engine logs the request BEFORE the LLM is called, so the exact count
	// is only known after this response comes back. This avoids needing the
	// rough char-based estimate in the engine.
	if l.q != nil && usage.PromptTokens > 0 && l.runID > 0 {
		runID := l.runID
		tokens := usage.PromptTokens
		go func() {
			for i := 0; i < 3; i++ {
				err := l.q.UpdateLastRequestEntryTokens(context.Background(), runID, tokens)
				if err == nil {
					break
				}
				fmt.Printf("UpdateLastRequestEntryTokens error (attempt %d): %v\n", i+1, err)
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}

	// Roll up per-run aggregates.
	if l.q != nil && l.runID > 0 {
		runID := l.runID
		delta := db.RunTokenStats{
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			ReasoningTokens:  usage.ReasoningTokens,
			ToolInputTokens:  usage.ToolInputTokens,
		}
		go func() {
			for i := 0; i < 3; i++ {
				err := l.q.AddRunTokenStats(context.Background(), runID, delta)
				if err == nil {
					break
				}
				fmt.Printf("AddRunTokenStats error (attempt %d): %v\n", i+1, err)
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}
}

func mustJSON(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// LogToolResultsFromRequest walks the OpenAI chat-completions messages
// array in an LLM request body and emits a tool_response log entry for
// every role:"tool" message it finds. The AI SDK includes each tool's
// output as a separate tool message in the next LLM call after the
// tool is executed, so this is the only place where the proxy can pair
// a tool_call (logged from a prior response) with its result.
//
// We also walk for Anthropic-style tool_result content blocks inside
// user messages (some providers model the result as a content block
// list rather than a separate message role).
func (l *ProxyLogger) LogToolResultsFromRequest(model, providerName string, messages []map[string]interface{}) {
	if l.q == nil || l.runID <= 0 || len(messages) == 0 {
		return
	}

	type toolResult struct {
		id      string
		name    string
		content string
	}
	var results []toolResult

	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role == "tool" {
			id, _ := msg["tool_call_id"].(string)
			if id == "" {
				// Some providers omit tool_call_id on tool messages;
				// hash the content to still dedupe across requests.
				id = "hash:" + contentHash(stringifyToolContent(msg["content"]))
			}
			if l.loggedToolResults[id] {
				continue
			}
			content := stringifyToolContent(msg["content"])
			results = append(results, toolResult{id: id, name: "", content: content})
			continue
		}
		// Anthropic-style: role:"user" with content:[{type:"tool_result", tool_use_id, content}]
		if role == "user" {
			if arr, ok := msg["content"].([]interface{}); ok {
				for _, blk := range arr {
					if m, ok := blk.(map[string]interface{}); ok {
						btype, _ := m["type"].(string)
						if btype == "tool_result" {
							id, _ := m["tool_use_id"].(string)
							if id == "" {
								id = "hash:" + contentHash(stringifyToolContent(m["content"]))
							}
							if l.loggedToolResults[id] {
								continue
							}
							content := stringifyToolContent(m["content"])
							results = append(results, toolResult{id: id, name: "", content: content})
						}
					}
				}
			}
		}
	}

	if len(results) == 0 {
		return
	}

	// Mark ids as seen BEFORE doing the work so a slow broadcast doesn't
	// cause a retry to re-emit them.
	for _, r := range results {
		l.loggedToolResults[r.id] = true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	l.file.WriteString(fmt.Sprintf("\n=== LLM Tool Results [%s] ===\n", ts))
	l.file.WriteString(fmt.Sprintf("Model: %s\n", model))
	l.file.WriteString(fmt.Sprintf("Provider: %s\n", providerName))
	l.file.WriteString(fmt.Sprintf("Count: %d\n", len(results)))
	l.file.WriteString("---\n")

	// Look up the engine-logged tool_call entries for this run so we
	// can pair each result with the tool name. If we can't find a
	// matching tool_call we still log the response — the name will
	// fall back to "tool".
	priorCalls := l.recentToolCalls(len(results) * 4)
	callIdx := 0

	for _, r := range results {
		name := r.name
		if name == "" {
			if callIdx < len(priorCalls) {
				name = priorCalls[callIdx].toolName
			}
			callIdx++
		}
		if name == "" {
			name = "tool"
		}
		outTokens := tokens.Estimate(r.content)
		preview := r.content
		if len(preview) > 2000 {
			preview = preview[:2000] + "…(truncated)"
		}
		l.file.WriteString(fmt.Sprintf("[%s] (%d tok)\n%s\n", name, outTokens, preview))

		// Emit a log entry. We don't pair by tool_call_id (the engine's
		// tool_call entries may not have it), we just append after the
		// prior tool_call entries; the frontend pairs them by name.
		l.broadcastLog("tool_response", preview, map[string]interface{}{
			"tool_name":     name,
			"output_tokens": outTokens,
		})
		go l.persistToolResponse(name, preview, outTokens)

		// Roll into the run-level aggregate so the header bar picks
		// it up.
		delta := db.RunTokenStats{ToolOutputTokens: outTokens}
		runID := l.runID
		go func() {
			for i := 0; i < 3; i++ {
				if err := l.q.AddRunTokenStats(context.Background(), runID, delta); err == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}
}

func stringifyToolContent(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// contentHash returns a short stable hash of a string. Used to dedupe
// tool result messages that lack a tool_call_id (some providers omit
// it). Collision risk is acceptable here because we only use this to
// avoid double-counting the same tool output, and a 16-char hex prefix
// is plenty for that purpose.
func contentHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// toolCallSnapshot is a lightweight record of a tool_call entry, used to
// pair proxy-side tool results with engine-side tool calls.
type toolCallSnapshot struct {
	toolName string
	id       string
}

// recentToolCalls loads the last N log entries and returns the tool_call
// ones (most recent first). The DB read is done synchronously so we can
// pair the results before broadcasting them.
func (l *ProxyLogger) recentToolCalls(n int) []toolCallSnapshot {
	if l.q == nil || l.runID <= 0 {
		return nil
	}
	run, err := l.q.GetRun(context.Background(), l.runID)
	if err != nil || run.LogEntries == "" {
		return nil
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(run.LogEntries), &entries); err != nil {
		return nil
	}
	var out []toolCallSnapshot
	for i := len(entries) - 1; i >= 0 && len(out) < n; i-- {
		if entries[i]["type"] == "tool_call" {
			name, _ := entries[i]["tool_name"].(string)
			id, _ := entries[i]["id"].(string)
			out = append(out, toolCallSnapshot{toolName: name, id: id})
		}
	}
	return out
}

// persistToolResponse is a fire-and-forget DB writer for tool_response
// entries. Kept separate from broadcastLog/persistLog so the file write
// and broadcast stay in the critical section.
func (l *ProxyLogger) persistToolResponse(toolName, content string, outputTokens int) {
	entry := map[string]interface{}{
		"type":          "tool_response",
		"content":       content,
		"ts":            time.Now().UTC().Format(time.RFC3339Nano),
		"tool_name":     toolName,
		"output_tokens": outputTokens,
	}
	for i := 0; i < 3; i++ {
		if err := l.q.AppendRunLogEntry(context.Background(), l.runID, entry); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (l *ProxyLogger) LogError(model, agentName, providerName string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	l.file.WriteString(fmt.Sprintf("\n=== LLM Error [%s] ===\n", ts))
	l.file.WriteString(fmt.Sprintf("Model: %s\n", model))
	l.file.WriteString(fmt.Sprintf("Agent: %s\n", agentName))
	l.file.WriteString(fmt.Sprintf("Provider: %s\n", providerName))
	l.file.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
	l.file.WriteString("\n")

	l.broadcastLog("error", fmt.Sprintf("[%s] %s: %s", agentName, model, err.Error()), map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
	})
	l.persistLog("error", fmt.Sprintf("[%s] %s: %s", agentName, model, err.Error()), map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
	})
}

// LogStall records a stream stall: writes to the file, broadcasts a
// dedicated "run_stalled" WebSocket event (separate from "run_log" so the
// frontend can react immediately), and persists an error entry.
func (l *ProxyLogger) LogStall(model, agentName, providerName string, stallDuration time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	l.file.WriteString(fmt.Sprintf("\n=== LLM Stream Stalled [%s] ===\n", ts))
	l.file.WriteString(fmt.Sprintf("Model: %s\n", model))
	l.file.WriteString(fmt.Sprintf("Agent: %s\n", agentName))
	l.file.WriteString(fmt.Sprintf("Provider: %s\n", providerName))
	l.file.WriteString(fmt.Sprintf("Stall duration: %v\n", stallDuration))
	l.file.WriteString("\n")

	msg := fmt.Sprintf("LLM stream stalled: no data for %v", stallDuration)
	l.broadcastLog("error", msg, map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
	})
	l.persistLog("error", msg, map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
	})

	if l.hub != nil && l.runID > 0 {
		l.hub.BroadcastEvent("run_stalled", map[string]interface{}{
			"run_id":         l.runID,
			"stall_duration": stallDuration.String(),
			"message":        msg,
		})
	}
}

func (l *ProxyLogger) Close() error {
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}
