package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// ExpandRunResult fetches the full explanation for a past run by ID.
type ExpandRunResult struct {
	fn func(ctx context.Context, runID int32) (string, error)
}

func NewExpandRunResult(fn func(ctx context.Context, runID int32) (string, error)) *ExpandRunResult {
	return &ExpandRunResult{fn: fn}
}

func (t *ExpandRunResult) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "expand_run_result",
			Description: "Retrieve the full detailed explanation for a previous run. Use this when the short result summary in your context is not enough to understand what a previous run did.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"run_id":{"type":"integer","description":"The numeric ID of the run whose explanation you want to read"}},"required":["run_id"]}`),
		},
	}
}

func (t *ExpandRunResult) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		RunID int32 `json:"run_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("expand_run_result: %w", err)
	}
	return t.fn(ctx, p.RunID)
}
