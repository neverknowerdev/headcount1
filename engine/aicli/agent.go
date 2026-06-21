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
	q              *db.Queries
	runID          int32
	logger         RunLogger
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
	ReasoningLevel string
	Queries        *db.Queries
	RunID          int32
	Logger         RunLogger
}

// New creates an Agent from a Config.
func New(cfg Config) *Agent {
	mode := cfg.Mode
	if mode == "" {
		mode = ModeMessageHistory
	}
	return &Agent{
		Client:         cfg.Client,
		Registry:       cfg.Registry,
		Mode:           mode,
		ProviderName:   cfg.ProviderName,
		AgentName:      cfg.AgentName,
		ReasoningLevel: cfg.ReasoningLevel,
		q:              cfg.Queries,
		runID:          cfg.RunID,
		logger:         cfg.Logger,
	}
}

// Run executes the agent loop starting with systemPrompt and userMessage.
// It returns the final text response from the LLM after all tool calls are
// exhausted, or an error if the loop fails.
func (a *Agent) Run(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	switch a.Mode {
	case ModeMessageHistory, "":
		return a.runMessageHistory(ctx, systemPrompt, userMessage, a.reasoningEffort())
	case ModeCompactThinking:
		return "", fmt.Errorf("compact_thinking mode is not yet implemented")
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
func (a *Agent) runMessageHistory(ctx context.Context, systemPrompt, userMessage, reasoningEffort string) (string, error) {
	history := []Message{}
	if systemPrompt != "" {
		history = append(history, Message{Role: "system", Content: systemPrompt})
	}
	history = append(history, Message{Role: "user", Content: userMessage})

	const maxTurns = 50
	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		req := ChatRequest{
			Messages:        history,
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

		results = append(results, Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    output,
		})
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
