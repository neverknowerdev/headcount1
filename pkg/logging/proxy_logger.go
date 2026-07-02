package logging

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/tokens"
)

// Usage is re-exported from pkg/tokens so call sites in this package can
// name the type without importing the tokens package directly.
type Usage = tokens.Usage

// SessionInfo describes one agent session for the purposes of StartSession's
// log file header — enough context to understand, from the file alone, what
// ran, with what model/tools/MCPs, and (if a sub-session) what spawned it.
type SessionInfo struct {
	SessionID       int32
	ParentSessionID *int32
	AgentName       string
	Role            string // session_type: orchestration, qa-research, implementation, testing, direct, proxy...
	Model           string
	Provider        string
	Tools           []string
	MCPs            []string
}

type ProxyLogger struct {
	mu     sync.Mutex
	runDir string // data/{company}/logs/{taskID}/run-{runID}/ — one file per session lives here
	// mainFile is the root/orchestration session's file, kept open for the
	// life of the run so sub-session "started" markers and any activity
	// between sub-sessions (e.g. escalation handling) always have somewhere
	// to land.
	mainFile     *os.File
	mainFilePath string
	// curFile is whichever file Log* calls currently write to — the main
	// file, or a sub-session's file while one is active.
	curFile     *os.File
	curFilePath string

	// mainSessionID is the DB session ID of the run's root session.
	// curSessionID/curParentSessionID track whichever session is currently
	// active and get stamped onto every broadcast/persisted log entry, so
	// the Run Log UI can group entries by session and render sub-sessions
	// as collapsible sub-timelines.
	mainSessionID      int32
	curSessionID       int32
	curParentSessionID *int32

	basePath string
	hub      interface{ BroadcastEvent(string, interface{}) }
	q        *db.Queries
	runID    int32
	// loggedToolResults dedupes tool result entries across LLM requests:
	// the same tool_call_id appears in every subsequent request's
	// messages[] (the conversation history keeps growing), but we only
	// want to log it once.
	loggedToolResults map[string]bool
	// toolCallNames maps tool_call_id → tool name, harvested from assistant
	// messages as requests/responses pass through, so tool results can be
	// labelled with the tool that actually produced them.
	toolCallNames map[string]string

	// sessionUsageByID accumulates token usage per session (keyed by the
	// session ID stamped in the header) so EndSession/Close can write a
	// per-session totals footer into each session's own file.
	sessionUsageByID map[int32]*sessionUsage
}

// sessionUsage aggregates token counts for one session's log footer.
type sessionUsage struct {
	prompt     int
	completion int
	reasoning  int
	cached     int
	toolOut    int
	llmCalls   int
}

func NewProxyLogger(basePath, companyShortName string, taskID int32, runID int32) (*ProxyLogger, error) {
	return NewProxyLoggerWithHub(basePath, companyShortName, taskID, runID, nil, nil)
}

// NewProxyLoggerWithHub creates a logger for one run. It creates (but
// doesn't yet write to) a dedicated folder for the run's session log files;
// call StartSession before the first Log* call to open the first session's
// file.
func NewProxyLoggerWithHub(basePath, companyShortName string, taskID int32, runID int32, hub interface{ BroadcastEvent(string, interface{}) }, q *db.Queries) (*ProxyLogger, error) {
	runDir := filepath.Join(basePath, "data", companyShortName, "logs", fmt.Sprintf("%d", taskID), fmt.Sprintf("run-%d", runID))
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	return &ProxyLogger{
		runDir:            runDir,
		basePath:          basePath,
		hub:               hub,
		q:                 q,
		runID:             runID,
		loggedToolResults: map[string]bool{},
		toolCallNames:     map[string]string{},
		sessionUsageByID:  map[int32]*sessionUsage{},
	}, nil
}

// FilePath returns the run's log folder (containing one file per session).
func (l *ProxyLogger) FilePath() string {
	return l.runDir
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeFileName(name string) string {
	if name == "" {
		return "agent"
	}
	return unsafeFileChars.ReplaceAllString(name, "_")
}

// StartSession opens (creating if needed) a dedicated log file for this
// session — "{AgentName}-{SessionID}.log" inside the run's folder — writes a
// header describing the session (model, role, tools, MCPs, parent), and
// makes it the active file for subsequent Log* calls.
//
// The first session started for a run is treated as the main/root session:
// its file stays open and active for the whole run. Every later session is
// treated as a sub-session: opening it also appends a one-line "starting new
// session" marker to the main file, so the main session's log doubles as a
// readable timeline of the whole run. Call EndSession when a sub-session
// finishes to switch logging back to the main file.
func (l *ProxyLogger) StartSession(info SessionInfo) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	fileName := fmt.Sprintf("%s-%d.log", sanitizeFileName(info.AgentName), info.SessionID)
	filePath := filepath.Join(l.runDir, fileName)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open session log file: %w", err)
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(f, "=== Session started [%s] ===\n", ts)
	fmt.Fprintf(f, "Run ID: %d\n", l.runID)
	fmt.Fprintf(f, "Session ID: %d\n", info.SessionID)
	if info.ParentSessionID != nil {
		fmt.Fprintf(f, "Parent session ID: %d\n", *info.ParentSessionID)
	}
	fmt.Fprintf(f, "Agent: %s\n", info.AgentName)
	fmt.Fprintf(f, "Role: %s\n", info.Role)
	fmt.Fprintf(f, "Model: %s\n", info.Model)
	fmt.Fprintf(f, "Provider: %s\n", info.Provider)
	if len(info.Tools) > 0 {
		fmt.Fprintf(f, "Tools: %s\n", strings.Join(info.Tools, ", "))
	} else {
		fmt.Fprintf(f, "Tools: (none)\n")
	}
	if len(info.MCPs) > 0 {
		fmt.Fprintf(f, "MCP servers: %s\n", strings.Join(info.MCPs, ", "))
	} else {
		fmt.Fprintf(f, "MCP servers: (all enabled MCPs)\n")
	}
	fmt.Fprintf(f, "%s\n\n", strings.Repeat("=", 70))

	if l.mainFile == nil {
		l.mainFile = f
		l.mainFilePath = filePath
		l.mainSessionID = info.SessionID
	} else {
		fmt.Fprintf(l.mainFile, "[%s] -> starting new session: %s (session #%d, role=%s)\n",
			ts, info.AgentName, info.SessionID, info.Role)

		// Emit a "session_start" entry into the CURRENTLY active session's
		// timeline (still the parent's at this point — curSessionID isn't
		// updated until below), so the Run Log UI can render it as a
		// collapsible marker in the parent's flat list. started_session_id
		// tells the UI which session's own entries (tagged separately below)
		// belong to the expanded view.
		sessionData := map[string]interface{}{
			"started_session_id": info.SessionID,
			"agent_name":         info.AgentName,
			"role":               info.Role,
			"model":              info.Model,
			"provider":           info.Provider,
			"tools":              info.Tools,
			"mcps":               info.MCPs,
		}
		contentBytes, _ := json.Marshal(sessionData)
		l.broadcastLog("session_start", string(contentBytes), nil)
		l.persistLog("session_start", string(contentBytes), nil)
	}

	l.curFile = f
	l.curFilePath = filePath
	l.curSessionID = info.SessionID
	l.curParentSessionID = info.ParentSessionID
	return nil
}

// addSessionUsage rolls token counts into the currently active session's
// aggregate. Callers must hold l.mu.
func (l *ProxyLogger) addSessionUsage(u Usage, llmCalls int) {
	su := l.sessionUsageByID[l.curSessionID]
	if su == nil {
		su = &sessionUsage{}
		l.sessionUsageByID[l.curSessionID] = su
	}
	su.prompt += u.PromptTokens
	su.completion += u.CompletionTokens
	su.reasoning += u.ReasoningTokens
	su.cached += u.CachedTokens
	su.llmCalls += llmCalls
}

// writeSessionUsageFooter writes the per-session token totals into w for the
// given session ID, if any usage was recorded. Callers must hold l.mu.
func (l *ProxyLogger) writeSessionUsageFooter(w io.Writer, sessionID int32) {
	su := l.sessionUsageByID[sessionID]
	if su == nil {
		return
	}
	fmt.Fprintf(w, "\n=== Session Token Totals === llm_calls=%d prompt=%d completion=%d reasoning=%d cached=%d tool_out=%d total=%d\n",
		su.llmCalls, su.prompt, su.completion, su.reasoning, su.cached, su.toolOut,
		su.prompt+su.completion)
}

// EndSession closes the current sub-session's file (if one is active) and
// switches subsequent Log* calls back to the main session's file. Safe to
// call even if the current session is already the main one.
func (l *ProxyLogger) EndSession() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.curFile != nil && l.curFile != l.mainFile {
		ts := time.Now().UTC().Format(time.RFC3339)
		l.writeSessionUsageFooter(l.curFile, l.curSessionID)
		fmt.Fprintf(l.curFile, "\n=== Session ended [%s] ===\n", ts)
		l.broadcastLog("session_end", "", nil)
		l.persistLog("session_end", "", nil)
		l.curFile.Close()
	}
	l.curFile = l.mainFile
	l.curFilePath = l.mainFilePath
	l.curSessionID = l.mainSessionID
	l.curParentSessionID = nil
}

// injectSessionFields stamps the currently active session's identity onto a
// log entry so the frontend can group entries by session and pair
// sub-session markers with their own timelines.
func (l *ProxyLogger) injectSessionFields(entry map[string]interface{}) {
	if l.curSessionID != 0 {
		entry["session_id"] = l.curSessionID
	}
	if l.curParentSessionID != nil {
		entry["parent_session_id"] = *l.curParentSessionID
	}
}

// out returns the writer that Log* calls should use: the current session's
// file if StartSession has been called, or io.Discard otherwise (so a
// caller that forgets to start a session doesn't crash — it just loses the
// file-based record while broadcast/DB persistence continue unaffected).
func (l *ProxyLogger) out() io.Writer {
	if l.curFile != nil {
		return l.curFile
	}
	return io.Discard
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
	l.injectSessionFields(entry)
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
	l.injectSessionFields(entry)
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
	io.WriteString(l.out(), fmt.Sprintf("\n=== LLM Request [%s] ===\n", ts))
	io.WriteString(l.out(), fmt.Sprintf("Model: %s\n", model))
	io.WriteString(l.out(), fmt.Sprintf("Agent: %s\n", agentName))
	io.WriteString(l.out(), fmt.Sprintf("Provider: %s\n", providerName))
	io.WriteString(l.out(), "---\n")
	l.out().Write(requestBody)
	io.WriteString(l.out(), "\n")

	l.broadcastLog("request", string(requestBody), map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
	})
	l.persistLog("request", string(requestBody), map[string]interface{}{
		"model":      model,
		"agent_name": agentName,
	})
}

func (l *ProxyLogger) LogResponse(model, agentName, providerName string, statusCode int, responseBody []byte, reasoningContent string, usage Usage) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	io.WriteString(l.out(), fmt.Sprintf("\n=== LLM Response [%s] ===\n", ts))
	io.WriteString(l.out(), fmt.Sprintf("Model: %s\n", model))
	io.WriteString(l.out(), fmt.Sprintf("Agent: %s\n", agentName))
	io.WriteString(l.out(), fmt.Sprintf("Provider: %s\n", providerName))
	io.WriteString(l.out(), fmt.Sprintf("Status: %d\n", statusCode))
	io.WriteString(l.out(), fmt.Sprintf("Tokens: prompt=%d completion=%d total=%d reasoning=%d\n",
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.ReasoningTokens))
	l.addSessionUsage(usage, 1)
	io.WriteString(l.out(), "---\n")
	// Write the raw response body unmodified, same as LogRequest.
	// The reasoning field is also folded into respData below so the
	// frontend can still render the reasoning panel; we don't need to
	// duplicate it in the file.
	l.out().Write(responseBody)
	io.WriteString(l.out(), "\n")

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
			"cached":     usage.CachedTokens,
		}
	}
	respBytes, _ := json.Marshal(respData)

	l.broadcastLog("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"agent_name":  agentName,
		"status_code": statusCode,
	})
	l.persistLog("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"agent_name":  agentName,
		"status_code": statusCode,
	})
}

func (l *ProxyLogger) LogStreamResponse(model, agentName, providerName string, content, reasoningContent string, toolCalls []map[string]interface{}, rawBody []byte, usage Usage) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	io.WriteString(l.out(), fmt.Sprintf("\n=== LLM Response [%s] ===\n", ts))
	io.WriteString(l.out(), fmt.Sprintf("Model: %s\n", model))
	io.WriteString(l.out(), fmt.Sprintf("Agent: %s\n", agentName))
	io.WriteString(l.out(), fmt.Sprintf("Provider: %s\n", providerName))
	io.WriteString(l.out(), fmt.Sprintf("Tokens: prompt=%d completion=%d total=%d reasoning=%d tool_in=%d\n",
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.ReasoningTokens, usage.ToolInputTokens))
	l.addSessionUsage(usage, 1)
	io.WriteString(l.out(), "---\n")
	// Write the raw SSE response body unmodified, the same way
	// LogRequest writes the raw request body. This makes the log file
	// a faithful replay of the wire traffic and avoids reformatting
	// the provider's JSON.
	if len(rawBody) > 0 {
		l.out().Write(rawBody)
	} else {
		// Fallback: no raw body was captured (e.g. very early failure
		// before the stream started). Synthesize a record from the
		// assembled fields so we still have something to inspect.
		if reasoningContent != "" {
			io.WriteString(l.out(), fmt.Sprintf("[Reasoning]\n%s\n", reasoningContent))
		}
		if content != "" {
			io.WriteString(l.out(), fmt.Sprintf("[Content]\n%s\n", content))
		}
		if len(toolCalls) > 0 {
			io.WriteString(l.out(), fmt.Sprintf("[ToolCalls]\n%s\n", mustJSON(toolCalls)))
		}
		if reasoningContent == "" && content == "" && len(toolCalls) == 0 {
			io.WriteString(l.out(), "(no content)\n")
		}
	}
	io.WriteString(l.out(), "\n")

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
	l.broadcastLog("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"agent_name":  agentName,
		"status_code": 200,
	})
	l.persistLog("response", string(respBytes), map[string]interface{}{
		"model":       model,
		"agent_name":  agentName,
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
		// Remember the id → name mapping so the matching tool result (seen
		// in a later request's messages) can be labelled correctly.
		if id, _ := tc["id"].(string); id != "" {
			l.toolCallNames[id] = name
		}
		argsJSON, _ := json.Marshal(tc["arguments"])
		inTokens := tokens.EstimateBytes(argsJSON)
		l.broadcastLog("tool_call", string(argsJSON), map[string]interface{}{
			"tool_name":     name,
			"input_tokens":  inTokens,
			"output_tokens": inTokens, // backwards-compat alias used by the UI today
		})
		l.persistLog("tool_call", string(argsJSON), map[string]interface{}{
			"tool_name":     name,
			"input_tokens":  inTokens,
			"output_tokens": inTokens,
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
	// Note: no l.q guard here — the file record must be written even when
	// there's no DB to persist to; DB-dependent steps below check l.q
	// themselves.
	if len(messages) == 0 {
		return
	}

	type toolResult struct {
		id      string
		name    string
		content string
	}
	var results []toolResult

	// First pass: harvest tool_call_id → tool name from assistant messages,
	// so each result can be labelled with the tool that actually produced it
	// (pairing positionally against recent tool_call log entries mislabels
	// results whenever calls interleave or fail).
	for _, msg := range messages {
		if role, _ := msg["role"].(string); role != "assistant" {
			continue
		}
		tcs, ok := msg["tool_calls"].([]interface{})
		if !ok {
			continue
		}
		for _, tcRaw := range tcs {
			tc, ok := tcRaw.(map[string]interface{})
			if !ok {
				continue
			}
			id, _ := tc["id"].(string)
			if id == "" {
				continue
			}
			if fn, ok := tc["function"].(map[string]interface{}); ok {
				if name, _ := fn["name"].(string); name != "" {
					l.toolCallNames[id] = name
				}
			}
		}
	}

	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role == "tool" {
			id, _ := msg["tool_call_id"].(string)
			// The engine's tool-result messages carry the tool name directly.
			name, _ := msg["name"].(string)
			if name == "" && id != "" {
				name = l.toolCallNames[id]
			}
			if id == "" {
				// Some providers omit tool_call_id on tool messages;
				// hash the content to still dedupe across requests.
				id = "hash:" + contentHash(stringifyToolContent(msg["content"]))
			}
			if l.loggedToolResults[id] {
				continue
			}
			content := stringifyToolContent(msg["content"])
			results = append(results, toolResult{id: id, name: name, content: content})
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
							name := ""
							if id != "" {
								name = l.toolCallNames[id]
							}
							if id == "" {
								id = "hash:" + contentHash(stringifyToolContent(m["content"]))
							}
							if l.loggedToolResults[id] {
								continue
							}
							content := stringifyToolContent(m["content"])
							results = append(results, toolResult{id: id, name: name, content: content})
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
	io.WriteString(l.out(), fmt.Sprintf("\n=== LLM Tool Results [%s] ===\n", ts))
	io.WriteString(l.out(), fmt.Sprintf("Model: %s\n", model))
	io.WriteString(l.out(), fmt.Sprintf("Provider: %s\n", providerName))
	io.WriteString(l.out(), fmt.Sprintf("Count: %d\n", len(results)))
	io.WriteString(l.out(), "---\n")

	// Fallback for results whose name couldn't be resolved from the
	// messages themselves: look up the engine-logged tool_call entries for
	// this run and match by tool_call_id. If nothing matches we still log
	// the response — the name falls back to "tool".
	var priorCalls []toolCallSnapshot
	for _, r := range results {
		if r.name == "" && !strings.HasPrefix(r.id, "hash:") {
			priorCalls = l.recentToolCalls(len(results) * 4)
			break
		}
	}

	for _, r := range results {
		name := r.name
		if name == "" {
			for _, pc := range priorCalls {
				if pc.id != "" && pc.id == r.id {
					name = pc.toolName
					break
				}
			}
		}
		if name == "" {
			name = "tool"
		}
		outTokens := tokens.Estimate(r.content)
		preview := r.content
		if len(preview) > 2000 {
			preview = preview[:2000] + "…(truncated)"
		}
		io.WriteString(l.out(), fmt.Sprintf("[%s] (%d tok)\n%s\n", name, outTokens, preview))

		if su := l.sessionUsageByID[l.curSessionID]; su != nil {
			su.toolOut += outTokens
		} else {
			l.sessionUsageByID[l.curSessionID] = &sessionUsage{toolOut: outTokens}
		}

		// Emit a log entry. We don't pair by tool_call_id (the engine's
		// tool_call entries may not have it), we just append after the
		// prior tool_call entries; the frontend pairs them by name.
		l.broadcastLog("tool_response", preview, map[string]interface{}{
			"tool_name":     name,
			"output_tokens": outTokens,
		})
		if l.q != nil && l.runID > 0 {
			go l.persistToolResponse(name, preview, outTokens, l.curSessionID, l.curParentSessionID)

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
func (l *ProxyLogger) persistToolResponse(toolName, content string, outputTokens int, sessionID int32, parentSessionID *int32) {
	entry := map[string]interface{}{
		"type":          "tool_response",
		"content":       content,
		"ts":            time.Now().UTC().Format(time.RFC3339Nano),
		"tool_name":     toolName,
		"output_tokens": outputTokens,
	}
	if sessionID != 0 {
		entry["session_id"] = sessionID
	}
	if parentSessionID != nil {
		entry["parent_session_id"] = *parentSessionID
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
	io.WriteString(l.out(), fmt.Sprintf("\n=== LLM Error [%s] ===\n", ts))
	io.WriteString(l.out(), fmt.Sprintf("Model: %s\n", model))
	io.WriteString(l.out(), fmt.Sprintf("Agent: %s\n", agentName))
	io.WriteString(l.out(), fmt.Sprintf("Provider: %s\n", providerName))
	io.WriteString(l.out(), fmt.Sprintf("Error: %s\n", err.Error()))
	io.WriteString(l.out(), "\n")

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
	io.WriteString(l.out(), fmt.Sprintf("\n=== LLM Stream Stalled [%s] ===\n", ts))
	io.WriteString(l.out(), fmt.Sprintf("Model: %s\n", model))
	io.WriteString(l.out(), fmt.Sprintf("Agent: %s\n", agentName))
	io.WriteString(l.out(), fmt.Sprintf("Provider: %s\n", providerName))
	io.WriteString(l.out(), fmt.Sprintf("Stall duration: %v\n", stallDuration))
	io.WriteString(l.out(), "\n")

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

// LogInfo writes a plain informational line to the log file and persists an
// "info" entry in the run's log_entries column.
func (l *ProxyLogger) LogInfo(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	io.WriteString(l.out(), fmt.Sprintf("[INFO %s] %s\n", ts, msg))

	l.broadcastLog("info", msg, nil)
	l.persistLog("info", msg, nil)
}

// LogWarn writes a warning line to the log file and persists a "warn" entry.
func (l *ProxyLogger) LogWarn(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	io.WriteString(l.out(), fmt.Sprintf("[WARN %s] %s\n", ts, msg))

	l.broadcastLog("warn", msg, nil)
	l.persistLog("warn", msg, nil)
}

// LogErrorMsg writes a plain error string to the log file and persists an
// "error" entry. Used by the NativeEngine when there is no model/agent context.
func (l *ProxyLogger) LogErrorMsg(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	io.WriteString(l.out(), fmt.Sprintf("\n=== Error [%s] ===\n%s\n", ts, msg))

	l.broadcastLog("error", msg, nil)
	l.persistLog("error", msg, nil)
}

func (l *ProxyLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var firstErr error
	if l.curFile != nil && l.curFile != l.mainFile {
		l.writeSessionUsageFooter(l.curFile, l.curSessionID)
		if err := l.curFile.Close(); err != nil {
			firstErr = err
		}
	}
	if l.mainFile != nil {
		l.writeSessionUsageFooter(l.mainFile, l.mainSessionID)
		if err := l.mainFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
