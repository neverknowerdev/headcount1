package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// VerificationResult is one verdict for an acceptance criterion or test case
// item, produced by the QA agent in a verification session.
type VerificationResult struct {
	List    string `json:"list"`    // "acceptance_criteria" or "test_cases"
	ID      int    `json:"id"`      // item id within the list
	Success bool   `json:"success"` // whether the item passed verification
	Error   string `json:"error,omitempty"`
}

// VerifyImplementation is the ONLY way to mark a task's acceptance criteria
// and test cases as passed. It spawns a fresh QA session (a different agent
// from the implementer) that inspects the workdir and artifacts, tests the
// implementation against every item, and reports per-item verdicts. The
// engine applies those verdicts to the task and returns them to the caller.
type VerifyImplementation struct {
	fn func(ctx context.Context, notes string) (string, error)
}

func NewVerifyImplementation(fn func(ctx context.Context, notes string) (string, error)) *VerifyImplementation {
	return &VerifyImplementation{fn: fn}
}

func (t *VerifyImplementation) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "verify_implementation",
			Description: "Verify the implementation against the task's acceptance criteria and test cases. " +
				"Spawns an independent QA session (a different agent from the implementer) that inspects the workdir " +
				"and artifacts, tests every item, and returns per-item verdicts as a JSON array. This is the ONLY way " +
				"to mark items as passed — finish_task refuses 'done'/'in-review' while items are unverified. " +
				"Call it after the implementation is complete; if items fail, fix them and call it again.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"notes":{
						"type":"string",
						"description":"Optional guidance for the QA agent: what changed, where to look, how to run things"
					}
				}
			}`),
		},
	}
}

func (t *VerifyImplementation) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Notes string `json:"notes"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("verify_implementation: %w", err)
	}
	out, err := t.fn(ctx, p.Notes)
	if err != nil {
		return "", fmt.Errorf("verify_implementation: %w", err)
	}
	return out, nil
}
