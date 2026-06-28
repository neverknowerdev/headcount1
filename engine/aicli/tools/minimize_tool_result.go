package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// MinimizeToolResult is a meta-tool the model calls after each tool result to
// provide a compact summary and key findings. The agent stores these and replaces
// the full output with the compact form in subsequent history sends.
type MinimizeToolResult struct {
	minimize func(toolCallID, summary string, keyFindings []string) error
}

// NewMinimizeToolResult creates a MinimizeToolResult tool. minimize is called
// with the tool_call_id, summary, and key_findings provided by the model.
func NewMinimizeToolResult(minimize func(toolCallID, summary string, keyFindings []string) error) *MinimizeToolResult {
	return &MinimizeToolResult{minimize: minimize}
}

func (t *MinimizeToolResult) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "minimize_tool_result",
			Description: "Compress a tool result to save tokens on future turns. Call this immediately after every tool call (before any other action). Provide a dense summary and the most important key_findings from the output.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"tool_call_id":{"type":"string","description":"ID of the tool call whose result you are compressing"},"summary":{"type":"string","description":"Brief dense summary of what the tool returned (1-3 sentences)"},"key_findings":{"type":"array","items":{"type":"string"},"description":"Most important individual findings to preserve for future reference"}},"required":["tool_call_id","summary","key_findings"]}`),
		},
	}
}

func (t *MinimizeToolResult) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ToolCallID  string   `json:"tool_call_id"`
		Summary     string   `json:"summary"`
		KeyFindings []string `json:"key_findings"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("minimize_tool_result: %w", err)
	}
	if p.ToolCallID == "" {
		return "", fmt.Errorf("minimize_tool_result: tool_call_id is required")
	}
	if err := t.minimize(p.ToolCallID, p.Summary, p.KeyFindings); err != nil {
		return "", fmt.Errorf("minimize_tool_result: %w", err)
	}
	return "Minimized.", nil
}
