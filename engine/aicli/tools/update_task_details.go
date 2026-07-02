package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/engine/aicli"
)

// UpdateTaskDetails lets the orchestrator record the outputs of the
// refinement stage as structured task fields, kept separate from the user's
// original description: the refined task description, acceptance criteria,
// and test cases.
type UpdateTaskDetails struct {
	fn func(ctx context.Context, refinedDescription, acceptanceCriteria, testCases string) error
}

func NewUpdateTaskDetails(fn func(ctx context.Context, refinedDescription, acceptanceCriteria, testCases string) error) *UpdateTaskDetails {
	return &UpdateTaskDetails{fn: fn}
}

func (t *UpdateTaskDetails) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "update_task_details",
			Description: "Record refinement results on the task as separate fields, without touching the user's original " +
				"description. Set refined_description after the refinement stage, and acceptance_criteria / test_cases " +
				"after they are defined. Fields left empty are not changed.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"refined_description":{
						"type":"string",
						"description":"The refined, unambiguous task description produced by the refinement stage (markdown)"
					},
					"acceptance_criteria":{
						"type":"string",
						"description":"Acceptance criteria as a checklist of verifiable statements (markdown)"
					},
					"test_cases":{
						"type":"string",
						"description":"Concrete test cases with steps and expected outcomes (markdown)"
					}
				}
			}`),
		},
	}
}

func (t *UpdateTaskDetails) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		RefinedDescription string `json:"refined_description"`
		AcceptanceCriteria string `json:"acceptance_criteria"`
		TestCases          string `json:"test_cases"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("update_task_details: %w", err)
	}
	if p.RefinedDescription == "" && p.AcceptanceCriteria == "" && p.TestCases == "" {
		return "", fmt.Errorf("at least one of refined_description, acceptance_criteria or test_cases is required")
	}
	if err := t.fn(ctx, p.RefinedDescription, p.AcceptanceCriteria, p.TestCases); err != nil {
		return "", fmt.Errorf("update_task_details: %w", err)
	}
	var updated []string
	if p.RefinedDescription != "" {
		updated = append(updated, "refined_description")
	}
	if p.AcceptanceCriteria != "" {
		updated = append(updated, "acceptance_criteria")
	}
	if p.TestCases != "" {
		updated = append(updated, "test_cases")
	}
	return fmt.Sprintf("Task details updated: %s.", strings.Join(updated, ", ")), nil
}
