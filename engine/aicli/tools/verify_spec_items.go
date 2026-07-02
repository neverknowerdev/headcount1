package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// SpecItemResult is one verification verdict for an acceptance criterion or
// test case item.
type SpecItemResult struct {
	List   string `json:"list"`   // "acceptance_criteria" or "test_cases"
	ID     int    `json:"id"`     // item id within the list
	Status string `json:"status"` // "passed" or "failed"
	Note   string `json:"note,omitempty"`
}

// VerifySpecItems marks acceptance criteria / test case items as passed or
// failed during the verification stage. finish_task refuses to complete a
// task while any item is still pending, so every item gets checked.
type VerifySpecItems struct {
	fn func(ctx context.Context, results []SpecItemResult) (string, error)
}

func NewVerifySpecItems(fn func(ctx context.Context, results []SpecItemResult) (string, error)) *VerifySpecItems {
	return &VerifySpecItems{fn: fn}
}

func (t *VerifySpecItems) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "verify_spec_items",
			Description: "Mark acceptance criteria / test case items as passed or failed after verification. " +
				"Every item must be checked before the task can be finished with 'done' or 'in-review'. " +
				"Base each verdict on actual verification results (e.g. a QA session), not on assumption.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"results":{
						"type":"array",
						"minItems":1,
						"items":{
							"type":"object",
							"properties":{
								"list":{"type":"string","enum":["acceptance_criteria","test_cases"],"description":"Which item list this verdict belongs to"},
								"id":{"type":"integer","description":"Item id within the list"},
								"status":{"type":"string","enum":["passed","failed"],"description":"Verification verdict"},
								"note":{"type":"string","description":"Short note, required for failed items (what went wrong)"}
							},
							"required":["list","id","status"]
						},
						"description":"Verdicts for one or more items; batch all results of a verification round into one call"
					}
				},
				"required":["results"]
			}`),
		},
	}
}

func (t *VerifySpecItems) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Results []SpecItemResult `json:"results"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("verify_spec_items: %w", err)
	}
	if len(p.Results) == 0 {
		return "", fmt.Errorf("results is required")
	}
	for _, r := range p.Results {
		if r.List != "acceptance_criteria" && r.List != "test_cases" {
			return "", fmt.Errorf("invalid list %q: must be acceptance_criteria or test_cases", r.List)
		}
		if r.Status != "passed" && r.Status != "failed" {
			return "", fmt.Errorf("invalid status %q for item %d: must be passed or failed", r.Status, r.ID)
		}
	}
	summary, err := t.fn(ctx, p.Results)
	if err != nil {
		return "", fmt.Errorf("verify_spec_items: %w", err)
	}
	return summary, nil
}
