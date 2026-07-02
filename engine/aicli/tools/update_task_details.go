package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/engine/aicli"
)

// maxSpecItems mirrors db.MaxSpecItems (this package must not import db).
const maxSpecItems = 10

// UpdateTaskDetails lets the orchestrator record the outputs of the
// refinement stage as structured task fields, kept separate from the user's
// original description: the refined task description plus acceptance criteria
// and test cases as individual item lists.
type UpdateTaskDetails struct {
	fn func(ctx context.Context, refinedDescription string, acceptanceCriteria, testCases []string) error
}

func NewUpdateTaskDetails(fn func(ctx context.Context, refinedDescription string, acceptanceCriteria, testCases []string) error) *UpdateTaskDetails {
	return &UpdateTaskDetails{fn: fn}
}

func (t *UpdateTaskDetails) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "update_task_details",
			Description: "Record refinement results on the task as separate fields, without touching the user's original " +
				"description. Set refined_description after the refinement stage, and acceptance_criteria / test_cases " +
				"after they are defined. Fields left empty are not changed. Acceptance criteria and test cases are ITEM " +
				"LISTS (max 10 each): every entry is one short, independently verifiable statement. Each item must later " +
				"be marked passed or failed via verify_spec_items before the task can finish.",
			Parameters: json.RawMessage(fmt.Sprintf(`{
				"type":"object",
				"properties":{
					"refined_description":{
						"type":"string",
						"description":"The refined, unambiguous task description: a few sentences of markdown, no headings or preamble"
					},
					"acceptance_criteria":{
						"type":"array",
						"items":{"type":"string"},
						"maxItems":%d,
						"description":"Verifiable one-line acceptance criteria, one statement per item (max %d)"
					},
					"test_cases":{
						"type":"array",
						"items":{"type":"string"},
						"maxItems":%d,
						"description":"One-line test cases in the form 'action -> expected result', one per item (max %d)"
					}
				}
			}`, maxSpecItems, maxSpecItems, maxSpecItems, maxSpecItems)),
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
		return "", fmt.Errorf("at least one of refined_description, acceptance_criteria or test_cases is required")
	}
	if len(p.AcceptanceCriteria) > maxSpecItems {
		return "", fmt.Errorf("acceptance_criteria has %d items; max is %d — merge or drop the least important ones", len(p.AcceptanceCriteria), maxSpecItems)
	}
	if len(p.TestCases) > maxSpecItems {
		return "", fmt.Errorf("test_cases has %d items; max is %d — merge or drop the least important ones", len(p.TestCases), maxSpecItems)
	}
	for _, item := range append(append([]string{}, p.AcceptanceCriteria...), p.TestCases...) {
		if strings.TrimSpace(item) == "" {
			return "", fmt.Errorf("spec items must be non-empty statements")
		}
	}
	if err := t.fn(ctx, p.RefinedDescription, p.AcceptanceCriteria, p.TestCases); err != nil {
		return "", fmt.Errorf("update_task_details: %w", err)
	}
	var updated []string
	if p.RefinedDescription != "" {
		updated = append(updated, "refined_description")
	}
	if len(p.AcceptanceCriteria) > 0 {
		updated = append(updated, fmt.Sprintf("acceptance_criteria (%d items)", len(p.AcceptanceCriteria)))
	}
	if len(p.TestCases) > 0 {
		updated = append(updated, fmt.Sprintf("test_cases (%d items)", len(p.TestCases)))
	}
	return fmt.Sprintf("Task details updated: %s.", strings.Join(updated, ", ")), nil
}
