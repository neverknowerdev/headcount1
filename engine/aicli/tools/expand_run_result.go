package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// ExpandRunResult fetches the full detailed handoff for a past run by run ID.
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
			Name: "expand_run_result",
			Description: "Retrieve the full detailed handoff (result_details) recorded by a previous run's finish_task. " +
				"Use this when a short result summary is not enough. Pass the RUN id exactly as given in subtask results " +
				"or past-run context (run_id, not a task or subtask id).",
			Parameters: json.RawMessage(`{"type":"object","properties":{"run_id":{"type":"integer","description":"The numeric RUN id (as shown in 'run #N' in subtask results and past-run context) — not a task id"}},"required":["run_id"]}`),
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
