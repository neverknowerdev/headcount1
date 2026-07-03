package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// UpdateTaskDetails records refinement outputs as structured task fields:
// a refined description plus acceptance criteria and test cases as item
// lists. Items are created in "pending" state; they are verified later by an
// independent run via verify_spec_items.
type UpdateTaskDetails struct {
	fn func(ctx context.Context, refinedDescription string, criteria, testCases []string) (string, error)
}

func NewUpdateTaskDetails(fn func(ctx context.Context, refinedDescription string, criteria, testCases []string) (string, error)) *UpdateTaskDetails {
	return &UpdateTaskDetails{fn: fn}
}

func (t *UpdateTaskDetails) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "update_task_details",
			Description: "Record task refinement as structured fields: refined_description (a few sentences), plus acceptance_criteria " +
				"and test_cases as item lists (max 10 each; aim for 3-7). Every item must be one short, independently verifiable " +
				"statement. Items are recorded as PENDING — they are verified later by a different run (e.g. a QA subtask) via " +
				"verify_spec_items. Condense specialist reports before recording; never paste full reports. " +
				"The user's original task description is never modified.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"refined_description":{"type":"string","description":"Refined task description, a few sentences"},
					"acceptance_criteria":{"type":"array","items":{"type":"string"},"description":"Acceptance criteria items, one short verifiable statement each (max 10)"},
					"test_cases":{"type":"array","items":{"type":"string"},"description":"Test case items in the form 'action -> expected result' (max 10)"}
				}
			}`),
		},
	}
}

func (t *UpdateTaskDetails) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		RefinedDescription string   `json:"refined_description"`
		AcceptanceCriteria []string `json:"acceptance_criteria"`
		TestCases          []string `json:"test_cases"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("update_task_details: %w", err)
	}
	if p.RefinedDescription == "" && len(p.AcceptanceCriteria) == 0 && len(p.TestCases) == 0 {
		return "", fmt.Errorf("update_task_details: provide refined_description, acceptance_criteria, or test_cases")
	}
	if len(p.AcceptanceCriteria) > 10 || len(p.TestCases) > 10 {
		return "", fmt.Errorf("update_task_details: max 10 items per list — condense before recording")
	}
	msg, err := t.fn(ctx, p.RefinedDescription, p.AcceptanceCriteria, p.TestCases)
	if err != nil {
		return "", fmt.Errorf("update_task_details: %w", err)
	}
	return msg, nil
}

// SpecVerdict is one verification verdict for a spec item.
type SpecVerdict struct {
	ID       int32  `json:"id"`
	Verdict  string `json:"verdict"`  // "passed" | "failed"
	Evidence string `json:"evidence"` // concrete evidence from the verified deliverable
}

// VerifySpecItems records verification verdicts for spec items. The engine
// enforces independence: a run may not verify items it defined itself.
type VerifySpecItems struct {
	fn func(ctx context.Context, verdicts []SpecVerdict) (string, error)
}

func NewVerifySpecItems(fn func(ctx context.Context, verdicts []SpecVerdict) (string, error)) *VerifySpecItems {
	return &VerifySpecItems{fn: fn}
}

func (t *VerifySpecItems) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "verify_spec_items",
			Description: "Record verification verdicts for acceptance criteria / test case items after ACTUALLY checking each one " +
				"against the real deliverable (read the artifact, run the test, inspect the code). Every verdict requires concrete " +
				"evidence — a quote, file:line reference, or command output. Never mark an item passed on assumption or on the " +
				"author's own claims. A run cannot verify items it defined itself.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"verdicts":{
						"type":"array",
						"items":{
							"type":"object",
							"properties":{
								"id":{"type":"integer","description":"Spec item ID (shown in the task context)"},
								"verdict":{"type":"string","enum":["passed","failed"]},
								"evidence":{"type":"string","description":"Concrete evidence: quote from the deliverable, file:line, or command output. Required."}
							},
							"required":["id","verdict","evidence"]
						}
					}
				},
				"required":["verdicts"]
			}`),
		},
	}
}

func (t *VerifySpecItems) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Verdicts []SpecVerdict `json:"verdicts"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("verify_spec_items: %w", err)
	}
	if len(p.Verdicts) == 0 {
		return "", fmt.Errorf("verify_spec_items: verdicts is required")
	}
	for _, v := range p.Verdicts {
		if len(v.Evidence) < 10 {
			return "", fmt.Errorf("verify_spec_items: item %d has no meaningful evidence — verify it for real, then cite what you saw", v.ID)
		}
	}
	msg, err := t.fn(ctx, p.Verdicts)
	if err != nil {
		return "", fmt.Errorf("verify_spec_items: %w", err)
	}
	return msg, nil
}
