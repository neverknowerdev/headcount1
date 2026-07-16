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
	"call_mcp_tool":     true,
	"discover_mcp_tool": true,
}

// blockingTools are allowed to run without the per-call watchdog timeout.
// ask_human, create_subtask, answer_subtask_question and ask_task_owner block
// by design (waiting on a human reply, a nested delegated session, or the
// task owner's answer) and can legitimately take hours. browser_use is
// stateful: it parents a headless browser on the first call's context so the
// browser survives for the whole run — a per-call cancel would kill it
// between turns and lose all navigation state. Its operations carry their
// own internal timeouts instead.
var blockingTools = map[string]bool{
	"ask_human":               true,
	"create_subtask":          true,
	"answer_subtask_question": true,
	"ask_task_owner":          true,
	"browser_use":             true,
}

// toolCallTimeout caps every non-blocking tool call so a single wedged tool
// (hung subprocess, dead MCP server, stuck network call) fails the call with
// a visible error instead of freezing the whole session forever.
const toolCallTimeout = 10 * time.Minute

const (
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
	// AppendEntry is the generic entry sink (JSONL file + metadata row +
	// WebSocket broadcast) used for tool_call/tool_response/error entries.
	AppendEntry(entryType, content string, extra map[string]interface{})
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
	MCPListingCostPerTurn int
	MCPServerListingCosts map[string]int
	// TerminalTools are tool names that end the run: once such a tool
	// executes successfully, the loop returns without another LLM call.
	TerminalTools map[string]bool
	q             *db.Queries
	runID         int32
	logger        RunLogger
}

// Config collects all the dependencies needed to create an Agent.
type Config struct {
	Client       *Client
	Registry     *Registry
	Mode         Mode
	ProviderName string
	AgentName    string
	// ReasoningLevel controls how much reasoning the LLM applies.
	// Accepted values: "low", "medium", "max". Empty = provider default.
	ReasoningLevel        string
	MCPListingCostPerTurn int
	MCPServerListingCosts map[string]int
	// TerminalTools lists tool names that end the run once they execute
	// successfully (e.g. "finish_task"), skipping the final wrap-up LLM call.
	TerminalTools []string
	Queries       *db.Queries
	RunID         int32
	Logger        RunLogger
}

// New creates an Agent from a Config.
func New(cfg Config) *Agent {
	mode := cfg.Mode
	if mode == "" {
		mode = ModeMessageHistory
	}
	terminal := make(map[string]bool, len(cfg.TerminalTools))
	for _, name := range cfg.TerminalTools {
		terminal[name] = true
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
		TerminalTools:         terminal,
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

		req := ChatRequest{
			Messages:        pruneHistory(history),
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
		toolMessages, terminalDone, err := a.executeToolCalls(ctx, assistantMsg.ToolCalls)
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

		// A terminal tool (e.g. finish_task) completed — the run is over.
		// Skip the extra wrap-up LLM round; the finish summary already exists.
		if terminalDone {
			return strings.TrimSpace(assistantMsg.Content), nil
		}
	}

	return "", fmt.Errorf("agent loop exceeded %d turns without a final answer", maxTurns)
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
		a.appendRunLog("tool_call", tc.Function.Arguments, map[string]interface{}{
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
		output, execErr := a.Registry.Execute(execCtx, tc.Function.Name, argsRaw)
		if execErr != nil {
			output = fmt.Sprintf("error: %v", execErr)
			// Surface the failure as a visible error entry in the run log,
			// in addition to returning it to the LLM as the tool result.
			a.appendRunLog("error", fmt.Sprintf("tool %s failed: %v", tc.Function.Name, execErr), map[string]interface{}{
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

	return results, terminalDone, nil
}

// appendRunLog routes a structured log entry through the session's run
// logger (JSONL file + run_log_entries row + WebSocket broadcast).
func (a *Agent) appendRunLog(entryType, content string, extra map[string]interface{}) {
	if a.logger == nil {
		return
	}
	a.logger.AppendEntry(entryType, content, extra)
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
