package aicli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/tokens"
)

type toolCallMessageIDContextKey struct{}

// WithToolCallMessageID annotates tool execution with the canonical JSONL
// message sequence of the assistant message that requested the tool.
func WithToolCallMessageID(ctx context.Context, messageID int64) context.Context {
	return context.WithValue(ctx, toolCallMessageIDContextKey{}, messageID)
}

// ToolCallMessageID returns the canonical message sequence for the current
// tool call, or zero when the call is executed outside a logged agent turn.
func ToolCallMessageID(ctx context.Context) int64 {
	messageID, _ := ctx.Value(toolCallMessageIDContextKey{}).(int64)
	return messageID
}

// ErrMaxTurns is returned (wrapped) when the agent loop hits its turn cap
// without producing a final answer. Callers can errors.Is against it to
// distinguish a runaway loop from a hard LLM/tool failure.
var ErrMaxTurns = errors.New("agent loop exceeded max turns without a final answer")

// ErrPaused is returned (wrapped) by RunWithHistory when a PauseRequested
// callback asked the loop to stop. It is not a failure: the caller is
// expected to persist the returned history and treat this as "paused, not
// finished" — a later RunWithHistory call seeded with that same history
// resumes exactly where this one left off.
var ErrPaused = errors.New("agent run paused")

// PauseRequested is polled once per turn, right after that turn's LLM
// response has fully arrived and before any of its tool calls execute — the
// one point in the loop where nothing is in flight and the conversation is
// in a state that's safe to snapshot. When it returns true and the turn
// produced tool calls (i.e. there is more work to do), RunWithHistory stops
// there instead of continuing, returning ErrPaused together with the history
// captured so far (including the just-received assistant message with its
// pending tool calls).
//
// If the turn's response has no tool calls, the run was finishing anyway —
// it completes normally rather than pausing one step from done. A tool call
// already in progress from a prior turn is never interrupted: this callback
// is never consulted mid-turn, only between turns.
//
// May be nil, in which case the loop never pauses.
type PauseRequested func() bool

// mcpDispatcherTools is the set of tool names used by the MCP dispatcher layer.
// Their responses are pruned from older history turns to avoid token accumulation.
var mcpDispatcherTools = map[string]bool{
	string(ToolCallMCP):     true,
	string(ToolDiscoverMCP): true,
}

// blockingTools are allowed to run without the per-call watchdog timeout.
// browser_use is stateful: it parents a headless browser on the first call's
// context so the browser survives for the whole run — a per-call cancel would
// kill it between turns and lose all navigation state. Its operations carry
// their own internal timeouts instead.
var blockingTools = map[string]bool{
	string(ToolBrowserUse): true,
}

// toolCallTimeout caps every non-blocking tool call so a single wedged tool
// (hung subprocess, dead MCP server, stuck network call) fails the call with
// a visible error instead of freezing the whole session forever.
const toolCallTimeout = 10 * time.Minute

const (
	// defaultMaxTurns is the safety cap for one agent session when the host
	// sets no budget of its own (see Config.MaxTurns).
	defaultMaxTurns = 300

	// maxToolOutputChars caps a single tool result appended to history.
	// Pathologically large outputs (full-file dumps, huge search results)
	// are truncated with a marker so the agent can re-query more narrowly.
	maxToolOutputChars = 60000

	// staleToolResultKeepChars is how much of a bulky old tool result
	// survives pruning once the conversation has moved past it.
	staleToolResultKeepChars = 1500

	// staleToolResultThreshold: old tool results larger than this get
	// truncated in requests (the in-memory history keeps the full content).
	staleToolResultThreshold = 4000

	// freshAssistantTurns is how many most-recent assistant turns keep their
	// tool results intact during pruning.
	freshAssistantTurns = 2
)

// ModelCall describes one provider round trip made by the agent loop.
type ModelCall struct {
	// Model is the model that answered, as the provider reported it.
	Model    string
	Usage    Usage
	Duration time.Duration
	// Err is set when the call failed.
	Err error
	// Sequence is the log sequence of the assistant message the call
	// produced; for a failed call, of the last message before it.
	Sequence int64
}

// RunLogger abstracts the logging dependencies that the agent needs.
// It is satisfied by *logging.ProxyLogger plus a few extra methods.
type RunLogger interface {
	LogRequest(model, agentName, providerName string, body []byte)
	LogResponse(model, providerName string, statusCode int, body []byte, reasoning string, usage logging.Usage)
	LogToolResultsFromRequest(model, providerName string, messages []map[string]interface{})
	LogConversationMessage(messageJSON []byte) int64
	Sync() error
	FilePath() string
}

// Agent runs a message-history agentic loop against an OpenAI-compatible
// LLM provider.  It supports tool calling, retry (via Client.Complete), and
// structured logging into the existing RunLog infrastructure.
type Agent struct {
	Client       *Client
	Registry     *Registry
	ProviderName string
	AgentName    string
	// ResumeNotice is appended after a restored pending tool result and before
	// the first post-resume LLM request. It is runtime-only metadata.
	ResumeNotice string
	// HistoryAlreadyLogged tells the agent that the supplied history was
	// restored from the canonical JSONL trajectory and must not be emitted a
	// second time. Fresh sessions log their initial system/user messages once.
	HistoryAlreadyLogged bool
	// MCPListingCostPerTurn is the estimated token cost of the MCP CompactListing
	// injected into the system prompt on every turn. Accumulated in RunTokenStats.
	MCPListingCostPerTurn int
	MCPServerListingCosts map[string]int
	// TerminalTools are tool names that end the run: once such a tool
	// executes successfully, the loop returns without another LLM call.
	TerminalTools        map[string]bool
	maxTurns             int
	q                    *db.Queries
	runID                int32
	logger               RunLogger
	conversationSequence int64
	beforeTurn           func(context.Context, []Message) ([]Message, error)
	toolsForTurn         func([]Message) []string
	onModelCall          func(ModelCall)
	onFinalText          func(context.Context, []Message) ([]Message, error)
	// asyncPersistence tracks the small, non-critical bookkeeping writes that
	// are launched while executing a tool. A canceled session joins them before
	// it returns so an E2E database wipe (or shutdown) cannot race a late log or
	// token-stat write from an already-stopped session.
	asyncPersistence sync.WaitGroup
}

// Config collects all the dependencies needed to create an Agent.
type Config struct {
	Client                *Client
	Registry              *Registry
	ProviderName          string
	AgentName             string
	ResumeNotice          string
	HistoryAlreadyLogged  bool
	MCPListingCostPerTurn int
	MCPServerListingCosts map[string]int
	// TerminalTools lists tool names that end the run once they execute
	// successfully (e.g. "finish_work"), skipping the final wrap-up LLM call.
	TerminalTools []string
	// MaxTurns caps the LLM round trips of one session. Zero uses the
	// package default.
	MaxTurns                    int
	Queries                     *db.Queries
	RunID                       int32
	Logger                      RunLogger
	InitialConversationSequence int64
	// BeforeTurn may add messages immediately before the next provider
	// request. It receives the complete conversation accumulated so far and
	// never interrupts an in-flight tool or LLM call.
	BeforeTurn func(context.Context, []Message) ([]Message, error)
	// ToolsForTurn may narrow the next request to the named tools, which the
	// model is then required to call. It is how a host makes one specific
	// call mandatory for a turn; returning nothing leaves the turn unrestricted.
	ToolsForTurn func(history []Message) []string
	// OnModelCall is told about every provider round trip the loop makes,
	// successful or not, so the host can account for it.
	OnModelCall func(ModelCall)
	// OnFinalText is called when the model answers without calling a tool,
	// which would otherwise end the run. The host may return messages to
	// continue the conversation with — a reminder of what is still expected —
	// or nothing to let the run end. It receives the whole conversation.
	OnFinalText func(context.Context, []Message) ([]Message, error)
}

// New creates an Agent from a Config.
func New(cfg Config) *Agent {
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	terminal := make(map[string]bool, len(cfg.TerminalTools))
	for _, name := range cfg.TerminalTools {
		terminal[name] = true
	}
	return &Agent{
		Client:                cfg.Client,
		Registry:              cfg.Registry,
		ProviderName:          cfg.ProviderName,
		AgentName:             cfg.AgentName,
		ResumeNotice:          cfg.ResumeNotice,
		HistoryAlreadyLogged:  cfg.HistoryAlreadyLogged,
		MCPListingCostPerTurn: cfg.MCPListingCostPerTurn,
		MCPServerListingCosts: cfg.MCPServerListingCosts,
		TerminalTools:         terminal,
		maxTurns:              maxTurns,
		q:                     cfg.Queries,
		runID:                 cfg.RunID,
		logger:                cfg.Logger,
		conversationSequence:  cfg.InitialConversationSequence,
		beforeTurn:            cfg.BeforeTurn,
		toolsForTurn:          cfg.ToolsForTurn,
		onModelCall:           cfg.OnModelCall,
		onFinalText:           cfg.OnFinalText,
	}
}

// ConversationSequence returns the JSONL sequence of the newest canonical
// message emitted by this agent. It is used as the durable checkpoint cursor.
func (a *Agent) ConversationSequence() int64 { return a.conversationSequence }

func (a *Agent) logConversationMessage(message Message) {
	if a.logger == nil {
		return
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return
	}
	a.conversationSequence = a.logger.LogConversationMessage(payload)
}

// BuildHistory assembles a session's opening conversation: the system prompt
// (if any) followed by the given messages. The result is a valid initial
// value for RunWithHistory.
func BuildHistory(systemPrompt string, messages []Message) []Message {
	history := []Message{}
	if systemPrompt != "" {
		history = append(history, Message{Role: "system", Content: systemPrompt})
	}
	history = append(history, messages...)
	return history
}

// RunWithHistory executes the agent loop starting from an existing message
// history — either a freshly built one (see BuildHistory) or a previously
// paused run's history being resumed. If the last message in history is an
// assistant message with tool calls that were never executed (exactly the
// state a pause leaves behind — see PauseRequested), those tool calls are
// executed first, before the loop continues as normal.
//
// Returns the final text answer and the full resulting history on normal
// completion. On early termination it returns the history as of that point
// alongside either ErrPaused (see PauseRequested) or another error.
func (a *Agent) RunWithHistory(ctx context.Context, history []Message, pause PauseRequested) (string, []Message, error) {
	defer func() {
		// Normal completion keeps bookkeeping writes off the model turn's
		// critical path. Cancellation is different: the caller is about to
		// tear down the session, so join the writes that were given the
		// canceled session context and let them exit before returning.
		if ctx.Err() != nil {
			a.asyncPersistence.Wait()
		}
	}()
	return a.runMessageHistory(ctx, history, pause)
}

// runMessageHistory maintains a rolling conversation history and sends the
// full history to the LLM on every turn.
//
// history may arrive "mid-turn": if it was captured by a prior pause (see
// PauseRequested), its last message is an assistant turn whose tool calls
// were never executed. That step is replayed first, before the main loop
// begins, so resuming is indistinguishable from having never paused.
func (a *Agent) runMessageHistory(ctx context.Context, history []Message, pause PauseRequested) (string, []Message, error) {
	if !a.HistoryAlreadyLogged {
		for _, message := range history {
			a.logConversationMessage(message)
		}
		a.HistoryAlreadyLogged = true
	}
	if n := len(history); n > 0 {
		last := history[n-1]
		if last.Role == "assistant" && len(last.ToolCalls) > 0 {
			toolMessages, terminalDone, err := a.executeToolCalls(ctx, last.ToolCalls)
			if err != nil {
				return "", history, fmt.Errorf("resume: tool execution failed: %w", err)
			}
			if a.logger != nil {
				a.logger.LogToolResultsFromRequest(a.Client.Model, a.ProviderName, msgsToMap(toolMessages))
			}
			history = append(history, toolMessages...)
			for _, message := range toolMessages {
				a.logConversationMessage(message)
			}
			if terminalDone {
				return strings.TrimSpace(last.Content), history, nil
			}
		}
	}
	if a.ResumeNotice != "" {
		notice := Message{Role: "system", Content: a.ResumeNotice}
		history = append(history, notice)
		a.logConversationMessage(notice)
		a.ResumeNotice = ""
	}

	for turn := 0; turn < a.maxTurns; turn++ {
		if ctx.Err() != nil {
			return "", history, ctx.Err()
		}
		if a.beforeTurn != nil {
			messages, hookErr := a.beforeTurn(ctx, history)
			if hookErr != nil {
				return "", history, fmt.Errorf("before-turn control message failed: %w", hookErr)
			}
			for _, message := range messages {
				history = append(history, message)
				a.logConversationMessage(message)
			}
		}

		req := ChatRequest{
			Messages: pruneHistory(history),
			Tools:    a.Registry.Defs(),
		}
		if a.toolsForTurn != nil {
			if only := a.toolsForTurn(history); len(only) > 0 {
				req.Tools = a.Registry.Filter(only).Defs()
				req.ToolChoice = ToolChoiceRequired
			}
		}

		// Log the outgoing request.
		reqBody, _ := json.Marshal(req)
		if a.logger != nil {
			a.logger.LogRequest(a.Client.Model, a.AgentName, a.ProviderName, reqBody)
		}

		callStarted := time.Now()
		resp, rawBody, err := a.Client.Complete(ctx, req)
		if err != nil {
			if a.onModelCall != nil {
				a.onModelCall(ModelCall{Err: err, Duration: time.Since(callStarted), Sequence: a.conversationSequence})
			}
			return "", history, fmt.Errorf("turn %d: LLM call failed: %w", turn, err)
		}
		callDuration := time.Since(callStarted)

		// Log the response.
		if a.logger != nil {
			usage := logging.Usage{
				PromptTokens:     resp.Usage.PromptTokens,
				CompletionTokens: resp.Usage.CompletionTokens,
				TotalTokens:      resp.Usage.TotalTokens,
				CachedTokens:     resp.Usage.PromptTokensDetails.CachedTokens,
				ReasoningTokens:  resp.Usage.CompletionTokensDetails.ReasoningTokens,
			}
			a.logger.LogResponse(a.Client.Model, a.ProviderName, 200, rawBody, "", usage)
		}

		// Persist token stats to the run record.
		if a.q != nil && a.runID > 0 {
			delta := db.RunTokenStats{
				PromptTokens:     resp.Usage.PromptTokens,
				CompletionTokens: resp.Usage.CompletionTokens,
				CachedTokens:     resp.Usage.PromptTokensDetails.CachedTokens,
				ReasoningTokens:  resp.Usage.CompletionTokensDetails.ReasoningTokens,
			}
			runID := a.runID
			a.asyncPersistence.Add(1)
			go func() {
				defer a.asyncPersistence.Done()
				for i := 0; i < 3; i++ {
					if err := a.q.AddRunTokenStats(ctx, runID, delta); err == nil {
						break
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(100 * time.Millisecond):
					}
				}
			}()
		}

		// Accumulate MCP listing overhead once per turn (it's in the system prompt every call).
		if a.MCPListingCostPerTurn > 0 && a.q != nil && a.runID > 0 {
			delta := db.RunTokenStats{
				MCPToolTokens:   a.MCPListingCostPerTurn,
				MCPServerTokens: a.MCPServerListingCosts,
			}
			runID := a.runID
			a.asyncPersistence.Add(1)
			go func() {
				defer a.asyncPersistence.Done()
				for i := 0; i < 3; i++ {
					if err := a.q.AddRunTokenStats(ctx, runID, delta); err == nil {
						break
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(100 * time.Millisecond):
					}
				}
			}()
		}

		if len(resp.Choices) == 0 {
			return "", history, fmt.Errorf("turn %d: LLM returned no choices", turn)
		}
		choice := resp.Choices[0]
		assistantMsg := choice.Message

		// Append the assistant turn to history before processing tool calls
		// so the next LLM call sees the full context.
		history = append(history, assistantMsg)
		a.logConversationMessage(assistantMsg)
		if a.onModelCall != nil {
			// The sequence is that of the assistant message just logged: it
			// locates this turn in the session's log.
			a.onModelCall(ModelCall{Model: resp.Model, Usage: resp.Usage, Duration: callDuration, Sequence: a.conversationSequence})
		}

		if len(assistantMsg.ToolCalls) == 0 {
			if a.onFinalText != nil {
				more, hookErr := a.onFinalText(ctx, history)
				if hookErr != nil {
					return "", history, fmt.Errorf("final-text hook failed: %w", hookErr)
				}
				if len(more) > 0 {
					history = append(history, more...)
					for _, message := range more {
						a.logConversationMessage(message)
					}
					continue
				}
			}
			// No tool call and nothing more expected: the text is the final answer.
			return strings.TrimSpace(assistantMsg.Content), history, nil
		}

		// Safe pause point: this turn's response has fully arrived and is
		// captured in history; nothing is executing right now. If asked to
		// pause, stop here instead of running this turn's tools — the caller
		// persists history and can resume later via RunWithHistory, which
		// replays exactly this step before continuing.
		if pause != nil && pause() {
			return "", history, ErrPaused
		}

		// Execute each tool call and build the tool-result messages.
		toolMessages, terminalDone, err := a.executeToolCalls(ctx, assistantMsg.ToolCalls)
		if err != nil {
			return "", history, fmt.Errorf("turn %d: tool execution failed: %w", turn, err)
		}

		// Log tool results from the messages we're about to append (so the
		// proxy logger can pair them with the prior tool_call entries).
		if a.logger != nil {
			toolMsgsAsMap := msgsToMap(toolMessages)
			a.logger.LogToolResultsFromRequest(a.Client.Model, a.ProviderName, toolMsgsAsMap)
		}

		history = append(history, toolMessages...)
		for _, message := range toolMessages {
			a.logConversationMessage(message)
		}

		// A terminal tool (e.g. finish_work) completed — the run is over.
		// Skip the extra wrap-up LLM round; the finish summary already exists.
		if terminalDone {
			return strings.TrimSpace(assistantMsg.Content), history, nil
		}
	}

	return "", history, fmt.Errorf("%w (%d turns)", ErrMaxTurns, a.maxTurns)
}

// executeToolCalls runs each ToolCall in the assistant message, logging each
// invocation and result, and returns the corresponding tool-result Messages.
// The bool result reports whether a terminal tool executed successfully.
func (a *Agent) executeToolCalls(ctx context.Context, calls []ToolCall) ([]Message, bool, error) {
	results := make([]Message, 0, len(calls))
	mcpServerTokens := map[string]int{}
	mcpTotalTokens := 0
	terminalDone := false

	for _, tc := range calls {
		argsRaw := json.RawMessage(tc.Function.Arguments)
		argTokens := tokens.EstimateBytes(argsRaw)

		// Log the tool call invocation.
		a.appendRunLog(ctx, "tool_call", tc.Function.Arguments, map[string]interface{}{
			"tool_name":     tc.Function.Name,
			"input_tokens":  argTokens,
			"output_tokens": argTokens, // backwards-compat alias
		})

		execCtx := ctx
		if !blockingTools[tc.Function.Name] {
			var cancel context.CancelFunc
			execCtx, cancel = context.WithTimeout(ctx, toolCallTimeout)
			defer cancel()
		}
		execCtx = WithToolCallMessageID(execCtx, a.conversationSequence)
		output, execErr := a.Registry.Execute(execCtx, tc.Function.Name, argsRaw)
		if execErr != nil {
			output = fmt.Sprintf("error: %v", execErr)
			// Surface the failure as a visible error entry in the run log,
			// in addition to returning it to the LLM as the tool result.
			a.appendRunLog(ctx, "error", fmt.Sprintf("tool %s failed: %v", tc.Function.Name, execErr), map[string]interface{}{
				"tool_name": tc.Function.Name,
			})
		} else if a.TerminalTools[tc.Function.Name] {
			terminalDone = true
		}

		// Cap pathologically large outputs so a single result can't blow up
		// the context; the agent is told how to get the rest.
		if len(output) > maxToolOutputChars {
			output = output[:maxToolOutputChars] +
				fmt.Sprintf("\n…[output truncated at %d chars — re-run %s with a narrower query to see more]", maxToolOutputChars, tc.Function.Name)
		}

		outTokens := tokens.Estimate(output)
		preview := output
		if len(preview) > 2000 {
			preview = preview[:2000] + "…(truncated)"
		}

		// Log the tool result.
		a.appendRunLog(ctx, "tool_response", preview, map[string]interface{}{
			"tool_name":     tc.Function.Name,
			"output_tokens": outTokens,
		})

		// Roll tool output tokens into the run aggregate.
		if a.q != nil && a.runID > 0 {
			delta := db.RunTokenStats{ToolOutputTokens: outTokens}
			runID := a.runID
			a.asyncPersistence.Add(1)
			go func() {
				defer a.asyncPersistence.Done()
				for i := 0; i < 3; i++ {
					if err := a.q.AddRunTokenStats(ctx, runID, delta); err == nil {
						break
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(100 * time.Millisecond):
					}
				}
			}()
		}

		// Track all MCP dispatcher calls: discover descriptions + actual tool calls/responses.
		if mcpDispatcherTools[tc.Function.Name] {
			mcpTotalTokens += outTokens
			var p struct {
				Server string `json:"server"`
			}
			if json.Unmarshal(argsRaw, &p) == nil && p.Server != "" {
				mcpServerTokens[p.Server] += outTokens
			}
		}

		results = append(results, Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    output,
		})
	}

	// Roll up MCP token stats as a single delta after all tool calls.
	if a.q != nil && a.runID > 0 && mcpTotalTokens > 0 {
		delta := db.RunTokenStats{MCPToolTokens: mcpTotalTokens, MCPServerTokens: mcpServerTokens}
		runID := a.runID
		a.asyncPersistence.Add(1)
		go func() {
			defer a.asyncPersistence.Done()
			for i := 0; i < 3; i++ {
				if err := a.q.AddRunTokenStats(ctx, runID, delta); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
		}()
	}

	return results, terminalDone, nil
}

func (a *Agent) appendRunLog(ctx context.Context, entryType, content string, extra map[string]interface{}) {
	if a.q == nil || a.runID <= 0 {
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
	runID := a.runID
	a.asyncPersistence.Add(1)
	go func() {
		defer a.asyncPersistence.Done()
		for i := 0; i < 3; i++ {
			if err := a.q.AppendRunLogEntry(ctx, runID, entry); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
}

// pruneHistory compacts the request payload without losing the in-memory
// history:
//   - MCP dispatcher results older than the last assistant turn are omitted
//     entirely (they are re-fetchable and often huge).
//   - Other bulky tool results (codegraph dumps, file reads, command output)
//     older than the last freshAssistantTurns assistant turns are truncated
//     to a head excerpt plus a marker telling the agent how to re-fetch.
//
// This keeps per-turn prompt growth roughly flat once results are digested,
// instead of re-sending every historical tool dump on every LLM call.
func pruneHistory(history []Message) []Message {
	// Find the cut-off: index of the freshAssistantTurns-th assistant message
	// from the end. Everything before it is "stale".
	seen := 0
	cutoff := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			seen++
			if seen == freshAssistantTurns {
				cutoff = i
				break
			}
		}
	}
	if cutoff <= 0 {
		return history
	}
	pruned := make([]Message, len(history))
	copy(pruned, history)
	for i := 0; i < cutoff; i++ {
		if pruned[i].Role != "tool" {
			continue
		}
		if mcpDispatcherTools[pruned[i].Name] {
			pruned[i].Content = "[omitted]"
			continue
		}
		if len(pruned[i].Content) > staleToolResultThreshold {
			pruned[i].Content = pruned[i].Content[:staleToolResultKeepChars] +
				"\n…[older tool output pruned to save context — call the tool again if you still need the full content]"
		}
	}
	return pruned
}

// msgsToMap converts []Message to []map[string]interface{} so the proxy
// logger's LogToolResultsFromRequest can walk them.
func msgsToMap(msgs []Message) []map[string]interface{} {
	out := make([]map[string]interface{}, len(msgs))
	for i, m := range msgs {
		b, _ := json.Marshal(m)
		var mm map[string]interface{}
		json.Unmarshal(b, &mm)
		out[i] = mm
	}
	return out
}
