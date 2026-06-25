package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// ExpandRunResult retrieves the detailed explanation for a previous run.
type ExpandRunResult struct {
	onExpand func(ctx context.Context, runID int32) (string, error)
}

func NewExpandRunResult(onExpand func(ctx context.Context, runID int32) (string, error)) *ExpandRunResult {
	return &ExpandRunResult{onExpand: onExpand}
}

func (t *ExpandRunResult) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "expand_run_result",
			Description: "Retrieve the full detailed explanation for a previous run. " +
				"Use this when the short result summary in your context is not enough to understand what a previous run did. " +
				"The run_id is shown in each run entry in your conversation history.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"run_id":{
						"type":"integer",
						"description":"The numeric ID of the run whose explanation you want to read"
					}
				},
				"required":["run_id"]
			}`),
		},
	}
}

func (t *ExpandRunResult) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		RunID int32 `json:"run_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	explanation, err := t.onExpand(ctx, p.RunID)
	if err != nil {
		return "", fmt.Errorf("expand_run_result: %w", err)
	}
	if explanation == "" {
		return "No detailed explanation available for this run.", nil
	}
	return explanation, nil
}
