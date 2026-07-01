package aicli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/tokens"
)

// mcpDispatcherTools is the set of tool names used by the MCP dispatcher layer.
// Their responses are pruned from older history turns to avoid token accumulation.
var mcpDispatcherTools = map[string]bool{
	"call_mcp_tool":    true,
	"discover_mcp_tool": true,
}

// Mode controls how the agent manages its conversation state.
type Mode string

const (
	// ModeMessageHistory (default) appends every turn to the history and
	// sends the full history to the LLM on each call.
	ModeMessageHistory Mode = "message_history"

	// ModeCompactThinking uses extended reasoning by passing a reasoning_effort
	// parameter on every request. History management is identical to
	// ModeMessageHistory; the difference is in how the LLM reasons internally.
	ModeCompactThinking Mode = "compact_thinking"

	// ModePlan10k is reserved for future implementation.
	ModePlan10k Mode = "plan-10k"
)

// RunLogger abstracts the logging dependencies that the agent needs.
// It is satisfied by *logging.ProxyLogger plus a few extra methods.
type RunLogger interface {
	LogRequest(model, agentName, providerName string, body []byte)
	LogResponse(model, agentName, providerName string, statusCode int, body []byte, reasoning string, usage logging.Usage)
	LogToolResultsFromRequest(model, providerName string, messages []map[string]interface{})
	FilePath() string
}

// Agent runs a message-history agentic loop against an OpenAI-compatible
// LLM provider.  It supports tool calling, retry (via Client.Complete), and
// structured logging into the existing RunLog infrastructure.
type Agent struct {
	Client         *Client
	Registry       *Registry
	Mode           Mode
	ProviderName   string
	AgentName      string
	ReasoningLevel string // "low", "medium", "max" → mapped to API values
	// MCPListingCostPerTurn is the estimated token cost of the MCP CompactListing
	// injected into the system prompt on every turn. Accumulated in RunTokenStats.
	MCPListingCostPerTurn    int
	MCPServerListingCosts    map[string]int
	// AskQuestionBatcher intercepts ask_question tool calls and processes them
	// as a batch. Set this when running in ask_mode to enable parallel question
	// batching through a researcher sub-session.
	AskQuestionBatcher *AskQuestionBatcher
	q                  *db.Queries
	runID              int32
	logger             RunLogger
}

// Config collects all the dependencies needed to create an Agent.
type Config struct {
	Client         *Client
	Registry       *Registry
	Mode           Mode
	ProviderName   string
	AgentName      string
	// ReasoningLevel controls how much reasoning the LLM applies.
	// Accepted values: "low", "medium", "max". Empty = provider default.
	ReasoningLevel        string
	MCPListingCostPerTurn int
	MCPServerListingCosts map[string]int
	// AskQuestionBatcher enables ask_mode batching. When set, ask_question tool
	// calls from a single LLM turn are collected and dispatched together via
	// RunBatch rather than executed individually through the Registry.
	AskQuestionBatcher *AskQuestionBatcher
	Queries            *db.Queries
	RunID              int32
	Logger             RunLogger
}

// New creates an Agent from a Config.
func New(cfg Config) *Agent {
	mode := cfg.Mode
	if mode == "" {
		mode = ModeMessageHistory
	}
	return &Agent{
		Client:                cfg.Client,
		Registry:              cfg.Registry,
		Mode:                  mode,
		ProviderName:          cfg.ProviderName,
		AgentName:             cfg.AgentName,
		ReasoningLevel:        cfg.ReasoningLevel,
		MCPListingCostPerTurn: cfg.MCPListingCostPerTurn,
		MCPServerListingCosts: cfg.MCPServerListingCosts,
		AskQuestionBatcher:    cfg.AskQuestionBatcher,
		q:                     cfg.Queries,
		runID:                 cfg.RunID,
		logger:                cfg.Logger,
	}
}

// Run executes the agent loop starting with systemPrompt and userMessage.
// It returns the final text response from the LLM after all tool calls are
// exhausted, or an error if the loop fails.
func (a *Agent) Run(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	return a.RunWithMessages(ctx, systemPrompt, []Message{{Role: "user", Content: userMessage}})
}

// RunWithMessages executes the agent loop with an explicit slice of initial
// messages (after the system prompt). Use this when the initial context
// contains multiple turns, e.g. task description + per-comment messages.
func (a *Agent) RunWithMessages(ctx context.Context, systemPrompt string, initialMessages []Message) (string, error) {
	switch a.Mode {
	case ModeMessageHistory, "":
		return a.runMessageHistory(ctx, systemPrompt, initialMessages, "")
	case ModeCompactThinking:
		return a.runMessageHistory(ctx, systemPrompt, initialMessages, a.reasoningEffort())
	default:
		return "", fmt.Errorf("unsupported agent mode: %s", a.Mode)
	}
}

// RunWithHistory continues the agent loop from an already-built message
// history. The history must include the system message (if any) and all prior
// turns. This is used to resume a session after a crash or handoff.
func (a *Agent) RunWithHistory(ctx context.Context, history []Message) (string, error) {
	switch a.Mode {
	case ModeMessageHistory, "":
		return a.continueHistory(ctx, history, "")
	case ModeCompactThinking:
		return a.continueHistory(ctx, history, a.reasoningEffort())
	default:
		return "", fmt.Errorf("unsupported agent mode: %s", a.Mode)
	}
}

// reasoningEffort maps the agent's ReasoningLevel to the OpenAI API value.
func (a *Agent) reasoningEffort() string {
	switch a.ReasoningLevel {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "max":
		return "high"
	default:
		return ""
	}
}

// runMessageHistory builds the initial history from systemPrompt and
// initialMessages, then delegates to continueHistory.
func (a *Agent) runMessageHistory(ctx context.Context, systemPrompt string, initialMessages []Message, reasoningEffort string) (string, error) {
	history := []Message{}
	if systemPrompt != "" {
		history = append(history, Message{Role: "system", Content: systemPrompt})
	}
	history = append(history, initialMessages...)
	return a.continueHistory(ctx, history, reasoningEffort)
}

// continueHistory runs the agentic loop starting from an already-assembled
// history slice. reasoningEffort is passed verbatim to the LLM when non-empty.
func (a *Agent) continueHistory(ctx context.Context, history []Message, reasoningEffort string) (string, error) {
	const maxTurns = 50
	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		req := ChatRequest{
			Messages:        pruneMCPHistory(history),
			Tools:           a.Registry.Defs(),
			ReasoningEffort: reasoningEffort,
		}

		// Log the outgoing request.
		reqBody, _ := json.Marshal(req)
		if a.logger != nil {
			a.logger.LogRequest(a.Client.Model, a.AgentName, a.ProviderName, reqBody)
		}

		resp, rawBody, err := a.Client.Complete(ctx, req)
		if err != nil {
			return "", fmt.Errorf("turn %d: LLM call failed: %w", turn, err)
		}

		// Log the response.
		if a.logger != nil {
			usage := logging.Usage{
				PromptTokens:     resp.Usage.PromptTokens,
				CompletionTokens: resp.Usage.CompletionTokens,
				TotalTokens:      resp.Usage.TotalTokens,
				CachedTokens:     resp.Usage.PromptTokensDetails.CachedTokens,
				ReasoningTokens:  resp.Usage.CompletionTokensDetails.ReasoningTokens,
			}
			a.logger.LogResponse(a.Client.Model, a.AgentName, a.ProviderName, 200, rawBody, "", usage)
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
			go func() {
				for i := 0; i < 3; i++ {
					if err := a.q.AddRunTokenStats(context.Background(), runID, delta); err == nil {
						break
					}
					time.Sleep(100 * time.Millisecond)
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
			go func() {
				for i := 0; i < 3; i++ {
					if err := a.q.AddRunTokenStats(context.Background(), runID, delta); err == nil {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}()
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("turn %d: LLM returned no choices", turn)
		}
		choice := resp.Choices[0]
		assistantMsg := choice.Message

		// Append the assistant turn to history before processing tool calls
		// so the next LLM call sees the full context.
		history = append(history, assistantMsg)

		if len(assistantMsg.ToolCalls) == 0 {
			// No tools to invoke — the assistant's text is the final answer.
			return strings.TrimSpace(assistantMsg.Content), nil
		}

		// Execute each tool call and build the tool-result messages.
		toolMessages, err := a.executeToolCalls(ctx, assistantMsg.ToolCalls)
		if err != nil {
			return "", fmt.Errorf("turn %d: tool execution failed: %w", turn, err)
		}

		history = append(history, toolMessages...)
	}

	return "", fmt.Errorf("agent loop exceeded %d turns without a final answer", maxTurns)
}

// executeToolCalls runs each ToolCall in the assistant message, logging each
// invocation and result, and returns the corresponding tool-result Messages in
// the same order as calls. When AskQuestionBatcher is set, all ask_question
// calls in a single turn are batched into one RunBatch invocation.
func (a *Agent) executeToolCalls(ctx context.Context, calls []ToolCall) ([]Message, error) {
	results := make([]Message, len(calls))

	// Separate ask_question calls from all others when a batcher is configured.
	type indexedCall struct {
		idx int
		tc  ToolCall
	}
	var askCalls []indexedCall
	var otherCalls []indexedCall

	if a.AskQuestionBatcher != nil {
		for i, tc := range calls {
			if tc.Function.Name == "ask_question" {
				askCalls = append(askCalls, indexedCall{i, tc})
			} else {
				otherCalls = append(otherCalls, indexedCall{i, tc})
			}
		}
	} else {
		for i, tc := range calls {
			otherCalls = append(otherCalls, indexedCall{i, tc})
		}
	}

	// Run ask_question batch if any.
	if len(askCalls) > 0 {
		questions := make([]string, len(askCalls))
		for i, ic := range askCalls {
			var p struct {
				Question string `json:"question"`
			}
			json.Unmarshal([]byte(ic.tc.Function.Arguments), &p) //nolint:errcheck
			questions[i] = p.Question
		}

		answers, err := a.AskQuestionBatcher.RunBatch(ctx, questions)
		if err != nil {
			// On batch error, fill all ask_question results with the error message.
			for _, ic := range askCalls {
				results[ic.idx] = Message{
					Role:       "tool",
					ToolCallID: ic.tc.ID,
					Name:       ic.tc.Function.Name,
					Content:    fmt.Sprintf("error: %v", err),
				}
			}
		} else {
			for i, ic := range askCalls {
				answer := ""
				if i < len(answers) {
					answer = answers[i]
				}
				a.appendRunLog("tool_call", ic.tc.Function.Arguments, map[string]interface{}{
					"tool_name": ic.tc.Function.Name,
				})
				a.appendRunLog("tool_response", answer, map[string]interface{}{
					"tool_name":     ic.tc.Function.Name,
					"output_tokens": tokens.Estimate(answer),
				})
				results[ic.idx] = Message{
					Role:       "tool",
					ToolCallID: ic.tc.ID,
					Name:       ic.tc.Function.Name,
					Content:    answer,
				}
			}
		}
	}

	// Run remaining (non-ask_question) tools with the standard logic.
	mcpServerTokens := map[string]int{}
	mcpTotalTokens := 0

	for _, ic := range otherCalls {
		tc := ic.tc
		argsRaw := json.RawMessage(tc.Function.Arguments)
		argTokens := tokens.EstimateBytes(argsRaw)

		// Log the tool call invocation.
		a.appendRunLog("tool_call", tc.Function.Arguments, map[string]interface{}{
			"tool_name":     tc.Function.Name,
			"input_tokens":  argTokens,
			"output_tokens": argTokens, // backwards-compat alias
		})

		output, execErr := a.Registry.Execute(ctx, tc.Function.Name, argsRaw)
		if execErr != nil {
			output = fmt.Sprintf("error: %v", execErr)
		}

		outTokens := tokens.Estimate(output)
		preview := output
		if len(preview) > 2000 {
			preview = preview[:2000] + "…(truncated)"
		}

		// Log the tool result.
		a.appendRunLog("tool_response", preview, map[string]interface{}{
			"tool_name":     tc.Function.Name,
			"output_tokens": outTokens,
		})

		// Roll tool output tokens into the run aggregate.
		if a.q != nil && a.runID > 0 {
			delta := db.RunTokenStats{ToolOutputTokens: outTokens}
			runID := a.runID
			go func() {
				for i := 0; i < 3; i++ {
					if err := a.q.AddRunTokenStats(context.Background(), runID, delta); err == nil {
						break
					}
					time.Sleep(100 * time.Millisecond)
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

		results[ic.idx] = Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    output,
		}
	}

	// Roll up MCP token stats as a single delta after all tool calls.
	if a.q != nil && a.runID > 0 && mcpTotalTokens > 0 {
		delta := db.RunTokenStats{MCPToolTokens: mcpTotalTokens, MCPServerTokens: mcpServerTokens}
		runID := a.runID
		go func() {
			for i := 0; i < 3; i++ {
				if err := a.q.AddRunTokenStats(context.Background(), runID, delta); err == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}

	// Log all tool results for the proxy logger so it can pair them with the
	// prior tool_call entries in its request log.
	if a.logger != nil {
		msgs := make([]Message, 0, len(results))
		for _, r := range results {
			if r.Role != "" {
				msgs = append(msgs, r)
			}
		}
		a.logger.LogToolResultsFromRequest(a.Client.Model, a.ProviderName, msgsToMap(msgs))
	}

	return results, nil
}

// appendRunLog persists a structured log entry to the run's log_entries column
// and broadcasts it over WebSocket via db.Queries. It is a fire-and-forget
// goroutine so it never blocks the agent loop.
func (a *Agent) appendRunLog(entryType, content string, extra map[string]interface{}) {
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
	go func() {
		for i := 0; i < 3; i++ {
			if err := a.q.AppendRunLogEntry(context.Background(), runID, entry); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

// pruneMCPHistory replaces old MCP tool responses with a placeholder so they
// don't consume tokens on every subsequent LLM call. Only the most recent
// batch of MCP results (those after the last assistant message) is kept intact.
func pruneMCPHistory(history []Message) []Message {
	// Find the last assistant message index.
	lastAssistant := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			lastAssistant = i
			break
		}
	}
	if lastAssistant <= 0 {
		return history
	}
	pruned := make([]Message, len(history))
	copy(pruned, history)
	for i := 0; i < lastAssistant; i++ {
		if pruned[i].Role == "tool" && mcpDispatcherTools[pruned[i].Name] {
			pruned[i].Content = "[omitted]"
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
