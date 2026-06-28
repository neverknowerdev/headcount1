package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// ExpandToolResult lets the model retrieve the full content of previously
// minimized tool calls. The agent also shows those entries at full fidelity in
// the next history send, then collapses them again.
type ExpandToolResult struct {
	expand func(toolCallIDs []string) (string, error)
}

// NewExpandToolResult creates an ExpandToolResult tool. expand receives the
// requested IDs, marks them for next-turn expansion, and returns their full
// formatted content.
func NewExpandToolResult(expand func(toolCallIDs []string) (string, error)) *ExpandToolResult {
	return &ExpandToolResult{expand: expand}
}

func (t *ExpandToolResult) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "expand_tool_result",
			Description: "Retrieve the full arguments and output of one or more previously minimized tool calls. The next message to the model will also include those tool calls at full fidelity; after that they collapse again.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"tool_call_ids":{"type":"array","items":{"type":"string"},"description":"IDs of the tool calls to expand"}},"required":["tool_call_ids"]}`),
		},
	}
}

func (t *ExpandToolResult) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ToolCallIDs []string `json:"tool_call_ids"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("expand_tool_result: %w", err)
	}
	if len(p.ToolCallIDs) == 0 {
		return "", fmt.Errorf("expand_tool_result: tool_call_ids must not be empty")
	}
	return t.expand(p.ToolCallIDs)
}
