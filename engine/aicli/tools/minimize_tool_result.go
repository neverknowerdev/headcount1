package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// MinimizeToolResult is a meta-tool the model calls after each tool result to
// provide a compact summary. The agent stores these and replaces the full output
// with the compact form in subsequent history sends.
type MinimizeToolResult struct {
	minimize func(toolCallID, summary string) error
}

// NewMinimizeToolResult creates a MinimizeToolResult tool. minimize is called
// with the tool_call_id and summary provided by the model.
func NewMinimizeToolResult(minimize func(toolCallID, summary string) error) *MinimizeToolResult {
	return &MinimizeToolResult{minimize: minimize}
}

func (t *MinimizeToolResult) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "minimize_tool_result",
			Description: "Compress a previous tool result to save tokens. Provide the tool_call_id and a dense one-sentence summary of what the tool returned.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"tool_call_id":{"type":"string","description":"ID of the tool call whose result you are compressing"},"summary":{"type":"string","description":"Dense one-sentence summary of what the tool returned"}},"required":["tool_call_id","summary"]}`),
		},
	}
}

func (t *MinimizeToolResult) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ToolCallID string `json:"tool_call_id"`
		Summary    string `json:"summary"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("minimize_tool_result: %w", err)
	}
	if p.ToolCallID == "" {
		return "", fmt.Errorf("minimize_tool_result: tool_call_id is required")
	}
	if err := t.minimize(p.ToolCallID, p.Summary); err != nil {
		return "", fmt.Errorf("minimize_tool_result: %w", err)
	}
	return "Minimized.", nil
}
