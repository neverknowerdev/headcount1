package logging

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/tokens"
)

// Usage is re-exported from pkg/tokens so call sites in this package can
// name the type without importing the tokens package directly.
type Usage = tokens.Usage

// ProxyLogger records every LLM interaction of a run. Each event goes to
// three sinks with one shared entry shape ({type, content, ts, ...extra}):
// a JSONL log file on disk (one entry per line, full fidelity — request
// entries embed the complete messages+tools payload and tool_response
// entries the untruncated tool output, so the file can be used directly as
// a trajectory for fine-tuning), a WebSocket broadcast for the live Run Log
// UI, and the runs.log_entries DB column (both with bounded previews).
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
	logFile := filepath.Join(logDir, fmt.Sprintf("run-%d.jsonl", runID))
	return newProxyLoggerAt(basePath, logDir, logFile, runID, hub, q)
}

// NewSessionLoggerWithHub creates a logger for an execution session. All
// sessions of one main run are grouped in a folder named after the root run:
// data/{company}/logs/{rootTaskID}/run-{rootRunID}/. The root session logs to
// main.jsonl; each delegated child session gets its own session-{runID}.jsonl.
func NewSessionLoggerWithHub(basePath, companyShortName string, rootTaskID, rootRunID, runID int32, hub interface{ BroadcastEvent(string, interface{}) }, q *db.Queries) (*ProxyLogger, error) {
	logDir := filepath.Join(basePath, "data", companyShortName, "logs", fmt.Sprintf("%d", rootTaskID), fmt.Sprintf("run-%d", rootRunID))
	fileName := "main.jsonl"
	if runID != rootRunID {
		fileName = fmt.Sprintf("session-%d.jsonl", runID)
	}
	return newProxyLoggerAt(basePath, logDir, filepath.Join(logDir, fileName), runID, hub, q)
}

func newProxyLoggerAt(basePath, logDir, logFile string, runID int32, hub interface{ BroadcastEvent(string, interface{}) }, q *db.Queries) (*ProxyLogger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

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

// makeEntry builds one structured log entry: {type, content, ts, ...extra}.
// The same entry shape is used for every sink a log line goes to — the run's
// .jsonl log file, the WebSocket stream and the runs.log_entries DB column.
func makeEntry(entryType, content string, extra map[string]interface{}) map[string]interface{} {
	entry := map[string]interface{}{
		"type":    entryType,
		"content": content,
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	for k, v := range extra {
		entry[k] = v
	}
	return entry
}

// writeFileEntry appends one entry as a single compact JSON line to the run's
// log file (JSONL). Every line is self-contained, so the file doubles as a
// machine-readable trajectory of the run: request entries carry the full
// messages+tools payload, response entries the assistant output, and
// tool_response entries the untruncated tool results. Callers must hold l.mu.
func (l *ProxyLogger) writeFileEntry(entry map[string]interface{}) {
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.file.Write(b)
	l.file.WriteString("\n")
}

func (l *ProxyLogger) broadcastEntry(entry map[string]interface{}) {
	if l.hub == nil || l.runID <= 0 {
		return
	}
	l.hub.BroadcastEvent("run_log", map[string]interface{}{
		"run_id": l.runID,
		"entry":  entry,
	})
}

func (l *ProxyLogger) persistEntry(entry map[string]interface{}) {
	if l.q == nil || l.runID <= 0 {
		return
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

// logEntry sends one entry to all three sinks: log file, WebSocket, DB.
// Callers must hold l.mu.
func (l *ProxyLogger) logEntry(entry map[string]interface{}) {
	l.writeFileEntry(entry)
	l.broadcastEntry(entry)
	l.persistEntry(entry)
}

func (l *ProxyLogger) LogRequest(model, agentName, providerName string, requestBody []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.logEntry(makeEntry("request", string(requestBody), map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
		"provider":   providerName,
	}))
}

func (l *ProxyLogger) LogResponse(model, providerName string, statusCode int, responseBody []byte, reasoningContent string, usage Usage) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Build a structured response payload. The shape matches what the
	// frontend's getAgentMessage already understands. The raw provider
	// body rides along unmodified in the "raw" field.
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
			"cached":     usage.CachedTokens,
		}
	}
	respBytes, _ := json.Marshal(respData)

	l.logEntry(makeEntry("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"provider":    providerName,
		"status_code": statusCode,
	}))
}

func (l *ProxyLogger) LogStreamResponse(model, providerName string, content, reasoningContent string, toolCalls []map[string]interface{}, rawBody []byte, usage Usage) {
	l.mu.Lock()
	defer l.mu.Unlock()

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
			"cached":     usage.CachedTokens,
		}
	}
	if len(rawBody) > 0 {
		// Forward the unmodified provider response (raw SSE stream) so
		// the original wire traffic stays inspectable (e.g. for providers
		// that emit things we don't have explicit fields for).
		respData["raw"] = string(rawBody)
	}
	respBytes, _ := json.Marshal(respData)
	l.logEntry(makeEntry("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"provider":    providerName,
		"status_code": 200,
	}))

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
		extra := map[string]interface{}{
			"tool_name":     name,
			"input_tokens":  inTokens,
			"output_tokens": inTokens, // backwards-compat alias used by the UI today
		}
		if id, _ := tc["id"].(string); id != "" {
			extra["tool_call_id"] = id
		}
		l.logEntry(makeEntry("tool_call", string(argsJSON), extra))
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
			CachedTokens:     usage.CachedTokens,
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

		extra := map[string]interface{}{
			"tool_name":     name,
			"output_tokens": outTokens,
		}
		if !strings.HasPrefix(r.id, "hash:") {
			extra["tool_call_id"] = r.id
		}

		// The file gets the full untruncated tool output so the JSONL log
		// stays a faithful trajectory of what the LLM actually saw. The
		// WebSocket/DB mirrors carry a bounded preview.
		entry := makeEntry("tool_response", r.content, extra)
		l.writeFileEntry(entry)

		preview := r.content
		if len(preview) > 2000 {
			preview = preview[:2000] + "…(truncated)"
		}
		previewEntry := map[string]interface{}{}
		for k, v := range entry {
			previewEntry[k] = v
		}
		previewEntry["content"] = preview
		l.broadcastEntry(previewEntry)
		l.persistEntry(previewEntry)

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

func (l *ProxyLogger) LogError(model, agentName, providerName string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.logEntry(makeEntry("error", fmt.Sprintf("[%s] %s: %s", agentName, model, err.Error()), map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
		"provider":   providerName,
	}))
}

// LogStall records a stream stall: writes to the file, broadcasts a
// dedicated "run_stalled" WebSocket event (separate from "run_log" so the
// frontend can react immediately), and persists an error entry.
func (l *ProxyLogger) LogStall(model, agentName, providerName string, stallDuration time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	msg := fmt.Sprintf("LLM stream stalled: no data for %v", stallDuration)
	l.logEntry(makeEntry("error", msg, map[string]interface{}{
		"model":          model,
		"agent_name":     agentName,
		"provider":       providerName,
		"stall_duration": stallDuration.String(),
	}))

	if l.hub != nil && l.runID > 0 {
		l.hub.BroadcastEvent("run_stalled", map[string]interface{}{
			"run_id":         l.runID,
			"stall_duration": stallDuration.String(),
			"message":        msg,
		})
	}
}

// LogSessionStarted records that a delegated child session began. The entry
// carries the child run id so the Run Log UI can render an expandable nested
// session block, and the file line points at the child's session log file.
func (l *ProxyLogger) LogSessionStarted(childRunID, childTaskID int32, agentName, title, logFile string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	content, _ := json.Marshal(map[string]interface{}{
		"run_id":     childRunID,
		"task_id":    childTaskID,
		"agent_name": agentName,
		"title":      title,
	})
	l.logEntry(makeEntry("session_started", string(content), map[string]interface{}{
		"run_id":     childRunID,
		"task_id":    childTaskID,
		"agent_name": agentName,
		"title":      title,
		"log_file":   logFile,
	}))
}

// LogSessionEnded records that a delegated child session finished.
func (l *ProxyLogger) LogSessionEnded(childRunID int32, status, result string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	content, _ := json.Marshal(map[string]interface{}{
		"run_id": childRunID,
		"status": status,
		"result": result,
	})
	l.logEntry(makeEntry("session_ended", string(content), map[string]interface{}{
		"run_id": childRunID,
		"status": status,
	}))
}

// LogModelSwitch records a model-group failover: the request to
// fromProvider/fromModel failed (or was rate limited) and the router is
// retrying with toProvider/toModel. Rendered as its own row in the Run Log.
func (l *ProxyLogger) LogModelSwitch(fromProvider, fromModel, toProvider, toModel, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	msg := fmt.Sprintf("Model switch: %s @ %s → %s @ %s (%s)", fromModel, fromProvider, toModel, toProvider, reason)
	l.logEntry(makeEntry("model_switch", msg, map[string]interface{}{
		"from_provider": fromProvider,
		"from_model":    fromModel,
		"to_provider":   toProvider,
		"to_model":      toModel,
		"reason":        reason,
	}))
}

// LogInfo writes a plain informational line to the log file and persists an
// "info" entry in the run's log_entries column.
func (l *ProxyLogger) LogInfo(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.logEntry(makeEntry("info", msg, nil))
}

// LogErrorMsg writes a plain error string to the log file and persists an
// "error" entry. Used by the NativeEngine when there is no model/agent context.
func (l *ProxyLogger) LogErrorMsg(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.logEntry(makeEntry("error", msg, nil))
}

func (l *ProxyLogger) Close() error {
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}
