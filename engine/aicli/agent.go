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
	LogResponse(model, providerName string, statusCode int, body []byte, reasoning string, usage logging.Usage)
	LogToolResultsFromRequest(model, providerName string, messages []map[string]interface{})
	FilePath() string
}

// ToolCompressionPrompt is appended to the system prompt when token-minimization
// tools are registered. It instructs the model to call minimize_tool_result after
// every tool result and to use expand_tool_result when it needs full content back.
const ToolCompressionPrompt = `
Tool result compression: After every tool call returns a result (except minimize_tool_result and expand_tool_result themselves), you MUST immediately call minimize_tool_result with a concise summary and key_findings array before taking any other action. If you need the full output of a previous tool call later, use expand_tool_result with the relevant tool_call_ids.`

// Agent runs a message-history agentic loop against an OpenAI-compatible
// LLM provider.  It supports tool calling, retry (via Client.Complete), and
// structured logging into the existing RunLog infrastructure.
type Agent struct {
	Client         *Client
	Registry       *Registry
	Store          *ToolResultStore // optional; enables tool-result minimization
	Mode           Mode
	ProviderName   string
	AgentName      string
	ReasoningLevel string // "low", "medium", "max" → mapped to API values
	// MCPListingCostPerTurn is the estimated token cost of the MCP CompactListing
	// injected into the system prompt on every turn. Accumulated in RunTokenStats.
	MCPListingCostPerTurn    int
	MCPServerListingCosts    map[string]int
	q                        *db.Queries
	runID                    int32
	logger                   RunLogger
}

// Config collects all the dependencies needed to create an Agent.
type Config struct {
	Client         *Client
	Registry       *Registry
	// Store enables tool-result minimization. When set, the agent stores each
	// tool result and transforms history to use compact summaries after the model
	// has minimized them via minimize_tool_result.
	Store          *ToolResultStore
	Mode           Mode
	ProviderName   string
	AgentName      string
	// ReasoningLevel controls how much reasoning the LLM applies.
	// Accepted values: "low", "medium", "max". Empty = provider default.
	ReasoningLevel           string
	MCPListingCostPerTurn    int
	MCPServerListingCosts    map[string]int
	Queries                  *db.Queries
	RunID                    int32
	Logger                   RunLogger
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
		Store:                 cfg.Store,
		Mode:                  mode,
		ProviderName:          cfg.ProviderName,
		AgentName:             cfg.AgentName,
		ReasoningLevel:        cfg.ReasoningLevel,
		MCPListingCostPerTurn: cfg.MCPListingCostPerTurn,
		MCPServerListingCosts: cfg.MCPServerListingCosts,
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

// runMessageHistory maintains a rolling conversation history and sends the
// full history to the LLM on every turn. reasoningEffort is passed verbatim
// as the request's reasoning_effort field when non-empty.
func (a *Agent) runMessageHistory(ctx context.Context, systemPrompt string, initialMessages []Message, reasoningEffort string) (string, error) {
	history := []Message{}
	if systemPrompt != "" {
		history = append(history, Message{Role: "system", Content: systemPrompt})
	}
	history = append(history, initialMessages...)

	const maxTurns = 50
	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		// Take expanded IDs (reset after this turn) and transform history.
		var expanded map[string]bool
		if a.Store != nil {
			expanded = a.Store.TakeExpanded()
		}
		msgs := pruneMCPHistory(history)
		if a.Store != nil {
			msgs = a.Store.TransformHistory(msgs, expanded)
		}

		// Exclude minimize_tool_result from the tools list when nothing needs
		// minimizing — the model should only see it when there is work to do.
		toolDefs := a.Registry.Defs()
		if a.Store != nil && !a.Store.HasUnminimized() {
			toolDefs = excludeTool(toolDefs, "minimize_tool_result")
		}

		req := ChatRequest{
			Messages:        msgs,
			Tools:           toolDefs,
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

		// Log tool results from the messages we're about to append (so the
		// proxy logger can pair them with the prior tool_call entries).
		if a.logger != nil {
			toolMsgsAsMap := msgsToMap(toolMessages)
			a.logger.LogToolResultsFromRequest(a.Client.Model, a.ProviderName, toolMsgsAsMap)
		}

		history = append(history, toolMessages...)
	}

	return "", fmt.Errorf("agent loop exceeded %d turns without a final answer", maxTurns)
}

// executeToolCalls runs each ToolCall in the assistant message, logging each
// invocation and result, and returns the corresponding tool-result Messages.
func (a *Agent) executeToolCalls(ctx context.Context, calls []ToolCall) ([]Message, error) {
	results := make([]Message, 0, len(calls))
	mcpServerTokens := map[string]int{}
	mcpTotalTokens := 0

	for _, tc := range calls {
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

		results = append(results, Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    output,
		})

		// Record the full result so the store can serve future minimization and
		// expansion. Skip meta-tools — their results are not subject to minimization.
		if a.Store != nil && !isMetaTool(tc.Function.Name) {
			a.Store.Store(tc.ID, tc.Function.Name, tc.Function.Arguments, output)
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

// excludeTool returns a copy of defs without the entry named name.
func excludeTool(defs []ToolDef, name string) []ToolDef {
	out := make([]ToolDef, 0, len(defs))
	for _, d := range defs {
		if d.Function.Name != name {
			out = append(out, d)
		}
	}
	return out
}

// isMetaTool reports whether name is an internal token-management tool that
// should not itself be stored in the ToolResultStore or be subject to minimization.
func isMetaTool(name string) bool {
	return name == "minimize_tool_result" || name == "expand_tool_result"
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
