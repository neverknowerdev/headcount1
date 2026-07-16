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
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/tokens"
)

// Usage is re-exported from pkg/tokens so call sites in this package can
// name the type without importing the tokens package directly.
type Usage = tokens.Usage

// ProxyLogger is the single sink for a run's log entries. Every entry is
// one JSON object per line in the run's .jsonl log file (the full content
// lives ONLY there), plus a lightweight metadata row in run_log_entries
// (type, timestamp, preview, token counts, and a byte pointer into the
// file), plus a live "run_log" WebSocket broadcast.
type ProxyLogger struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	basePath string
	offset   int64 // current end of file; next entry's ByteOffset
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
	logDir := filesystem.NewPaths(basePath).TaskLogsDir(companyShortName, taskID)
	logFile := filepath.Join(logDir, fmt.Sprintf("run-%d.jsonl", runID))
	return newProxyLoggerAt(basePath, logDir, logFile, runID, hub, q)
}

// NewSessionLoggerWithHub creates a logger for an execution session. All
// sessions of one main run are grouped in a folder named after the root run:
// logs/{company}/{rootTaskID}/run-{rootRunID}/. The root session logs to
// main.jsonl; each delegated child session gets its own session-{runID}.jsonl.
func NewSessionLoggerWithHub(basePath, companyShortName string, rootTaskID, rootRunID, runID int32, hub interface{ BroadcastEvent(string, interface{}) }, q *db.Queries) (*ProxyLogger, error) {
	logDir := filesystem.NewPaths(basePath).RunLogsDir(companyShortName, rootTaskID, rootRunID)
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

	// The file is opened in append mode: when a run's file already has
	// content (e.g. a second logger for the same run), new entries start at
	// the current end.
	offset := int64(0)
	if info, err := f.Stat(); err == nil {
		offset = info.Size()
	}

	return &ProxyLogger{
		file:              f,
		filePath:          logFile,
		basePath:          basePath,
		offset:            offset,
		hub:               hub,
		q:                 q,
		runID:             runID,
		loggedToolResults: map[string]bool{},
	}, nil
}

func (l *ProxyLogger) FilePath() string {
	return l.filePath
}

const previewLen = 300

func preview(content string) string {
	if len(content) <= previewLen {
		return content
	}
	return content[:previewLen] + "…"
}

// AppendEntry is the single write path for log entries: one JSONL line
// (full content), one run_log_entries metadata row pointing at it, one
// WebSocket broadcast. Safe for concurrent use.
func (l *ProxyLogger) AppendEntry(entryType, content string, extra map[string]interface{}) {
	ts := time.Now().UTC()

	entry := map[string]interface{}{
		"type":    entryType,
		"content": content,
		"ts":      ts.Format(time.RFC3339Nano),
	}
	for k, v := range extra {
		entry[k] = v
	}

	line, err := json.Marshal(entry)
	if err != nil {
		fmt.Printf("run log: failed to marshal entry: %v\n", err)
		return
	}
	line = append(line, '\n')

	l.mu.Lock()
	byteOffset := l.offset
	if _, err := l.file.Write(line); err != nil {
		fmt.Printf("run log: failed to write %s: %v\n", l.filePath, err)
	} else {
		l.offset += int64(len(line))
	}
	l.mu.Unlock()

	if l.q != nil && l.runID > 0 {
		row := db.RunLogEntry{
			RunID:       l.runID,
			Type:        entryType,
			Ts:          ts,
			Preview:     preview(content),
			ByteOffset:  byteOffset,
			ByteLen:     int32(len(line)),
			LogFilePath: l.filePath,
		}
		applyEntryMetadata(&row, extra)
		if err := l.q.CreateRunLogEntry(context.Background(), row); err != nil {
			fmt.Printf("run log: failed to insert entry row: %v\n", err)
		}
	}

	if l.hub != nil && l.runID > 0 {
		l.hub.BroadcastEvent("run_log", map[string]interface{}{
			"run_id": l.runID,
			"entry":  entry,
		})
	}
}

// applyEntryMetadata copies the well-known extra fields into the metadata
// row's typed columns.
func applyEntryMetadata(row *db.RunLogEntry, extra map[string]interface{}) {
	for k, v := range extra {
		switch k {
		case "tool_name":
			row.ToolName, _ = v.(string)
		case "model":
			row.Model, _ = v.(string)
		case "agent_name":
			row.AgentName, _ = v.(string)
		case "status_code":
			row.StatusCode = toInt(v)
		case "prompt_tokens", "est_prompt_tokens":
			row.PromptTokens = toInt(v)
		case "input_tokens":
			row.InputTokens = toInt(v)
		case "output_tokens":
			row.OutputTokens = toInt(v)
		case "run_id":
			// session_started/session_ended carry the child run's id.
			child := int32(toInt(v))
			row.ChildRunID = &child
		}
	}
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func (l *ProxyLogger) LogRequest(model, agentName, providerName string, requestBody []byte) {
	l.AppendEntry("request", string(requestBody), map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
		"provider":   providerName,
	})
}

func (l *ProxyLogger) LogResponse(model, providerName string, statusCode int, responseBody []byte, reasoningContent string, usage Usage) {
	// Build a structured response payload. The shape matches what the
	// frontend's getAgentMessage already understands.
	respData := map[string]interface{}{}
	if len(responseBody) > 0 {
		// Forward the original LLM response body as the "raw" content;
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

	l.AppendEntry("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"provider":    providerName,
		"status_code": statusCode,
	})
}

func (l *ProxyLogger) LogStreamResponse(model, providerName string, content, reasoningContent string, toolCalls []map[string]interface{}, rawBody []byte, usage Usage) {
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
		// Forward the unmodified provider response so the frontend
		// can still inspect the original shape (e.g. for providers
		// that emit things we don't have explicit fields for).
		respData["raw"] = string(rawBody)
	}
	respBytes, _ := json.Marshal(respData)
	l.AppendEntry("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"provider":    providerName,
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
		l.AppendEntry("tool_call", string(argsJSON), map[string]interface{}{
			"tool_name":     name,
			"input_tokens":  inTokens,
			"output_tokens": inTokens, // backwards-compat alias used by the UI today
		})
	}

	// Inject the actual prompt_tokens into the engine's "request" entry. The
	// engine logs the request BEFORE the LLM is called, so the exact count
	// is only known after this response comes back.
	if l.q != nil && usage.PromptTokens > 0 && l.runID > 0 {
		if err := l.q.SetFirstRequestPromptTokens(context.Background(), l.runID, usage.PromptTokens); err != nil {
			fmt.Printf("SetFirstRequestPromptTokens error: %v\n", err)
		}
	}

	// Roll up per-run aggregates.
	l.addTokenStats(db.RunTokenStats{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		ToolInputTokens:  usage.ToolInputTokens,
		CachedTokens:     usage.CachedTokens,
	})
}

// addTokenStats rolls a delta into the run's aggregate token stats,
// retrying a couple of times on transient DB contention.
func (l *ProxyLogger) addTokenStats(delta db.RunTokenStats) {
	if l.q == nil || l.runID <= 0 || delta.IsEmpty() {
		return
	}
	runID := l.runID
	go func() {
		for i := 0; i < 3; i++ {
			if err := l.q.AddRunTokenStats(context.Background(), runID, delta); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
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

	// Look up the prior tool_call entries for this run so we can pair each
	// result with the tool name. If we can't find a matching tool_call we
	// still log the response — the name will fall back to "tool".
	priorCalls := l.recentToolCalls(len(results) * 4)
	callIdx := 0

	for _, r := range results {
		name := r.name
		if name == "" {
			if callIdx < len(priorCalls) {
				name = priorCalls[callIdx].ToolName
			}
			callIdx++
		}
		if name == "" {
			name = "tool"
		}
		outTokens := tokens.Estimate(r.content)
		content := r.content
		if len(content) > 2000 {
			content = content[:2000] + "…(truncated)"
		}

		// We don't pair by tool_call_id (the engine's tool_call entries may
		// not have it), we just append after the prior tool_call entries;
		// the frontend pairs them by name.
		l.AppendEntry("tool_response", content, map[string]interface{}{
			"tool_name":     name,
			"output_tokens": outTokens,
		})

		// Roll into the run-level aggregate so the header bar picks it up.
		l.addTokenStats(db.RunTokenStats{ToolOutputTokens: outTokens})
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

// recentToolCalls loads the run's most recent tool_call entries (most
// recent first) from run_log_entries. The DB read is done synchronously so
// we can pair the results before broadcasting them.
func (l *ProxyLogger) recentToolCalls(n int) []db.RunLogEntry {
	if l.q == nil || l.runID <= 0 {
		return nil
	}
	entries, err := l.q.ListRecentToolCallEntries(context.Background(), l.runID, n)
	if err != nil {
		return nil
	}
	return entries
}

func (l *ProxyLogger) LogError(model, agentName, providerName string, err error) {
	l.AppendEntry("error", fmt.Sprintf("[%s] %s: %s", agentName, model, err.Error()), map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
		"provider":   providerName,
	})
}

// LogStall records a stream stall: logs an error entry and broadcasts a
// dedicated "run_stalled" WebSocket event (separate from "run_log" so the
// frontend can react immediately).
func (l *ProxyLogger) LogStall(model, agentName, providerName string, stallDuration time.Duration) {
	msg := fmt.Sprintf("LLM stream stalled: no data for %v", stallDuration)
	l.AppendEntry("error", msg, map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
		"provider":   providerName,
	})

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
// session block.
func (l *ProxyLogger) LogSessionStarted(childRunID, childTaskID int32, agentName, title, logFile string) {
	content, _ := json.Marshal(map[string]interface{}{
		"run_id":     childRunID,
		"task_id":    childTaskID,
		"agent_name": agentName,
		"title":      title,
		"log_file":   logFile,
	})
	l.AppendEntry("session_started", string(content), map[string]interface{}{
		"run_id":     childRunID,
		"task_id":    childTaskID,
		"agent_name": agentName,
		"title":      title,
	})
}

// LogSessionEnded records that a delegated child session finished.
func (l *ProxyLogger) LogSessionEnded(childRunID int32, status, result string) {
	content, _ := json.Marshal(map[string]interface{}{
		"run_id": childRunID,
		"status": status,
		"result": result,
	})
	l.AppendEntry("session_ended", string(content), map[string]interface{}{
		"run_id": childRunID,
		"status": status,
	})
}

// LogModelSwitch records a model-group failover: the request to
// fromProvider/fromModel failed (or was rate limited) and the router is
// retrying with toProvider/toModel. Rendered as its own row in the Run Log.
func (l *ProxyLogger) LogModelSwitch(fromProvider, fromModel, toProvider, toModel, reason string) {
	msg := fmt.Sprintf("Model switch: %s @ %s → %s @ %s (%s)", fromModel, fromProvider, toModel, toProvider, reason)
	l.AppendEntry("model_switch", msg, map[string]interface{}{
		"from_provider": fromProvider,
		"from_model":    fromModel,
		"to_provider":   toProvider,
		"to_model":      toModel,
		"reason":        reason,
	})
}

// LogInfo logs a plain informational entry.
func (l *ProxyLogger) LogInfo(msg string) {
	l.AppendEntry("info", msg, nil)
}

// LogErrorMsg logs a plain error entry. Used by the NativeEngine when there
// is no model/agent context.
func (l *ProxyLogger) LogErrorMsg(msg string) {
	l.AppendEntry("error", msg, nil)
}

func (l *ProxyLogger) Close() error {
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}
