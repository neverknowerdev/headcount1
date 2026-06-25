package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// FinishTaskExecution is called by the agent at the end of every run to record
// the outcome, update the task status, and leave a visible summary for the user.
type FinishTaskExecution struct {
	onFinish func(ctx context.Context, status, description, explanation string) error
}

func NewFinishTaskExecution(onFinish func(ctx context.Context, status, description, explanation string) error) *FinishTaskExecution {
	return &FinishTaskExecution{onFinish: onFinish}
}

func (t *FinishTaskExecution) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "finish_task_execution",
			Description: "MUST be called at the end of every run to record the outcome and update the task status. " +
				"Use 'in-review' when the work is done and ready for human review. " +
				"Use 'blocked' when you are stuck and need user input. " +
				"Use 'done' when the task is fully complete and no review is needed. " +
				"Use 'refinement' when you need clarification before you can proceed. " +
				"task_status_description is a short one-sentence summary shown to the user. " +
				"task_status_explanation is a detailed markdown report of everything you did.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"task_status":{
						"type":"string",
						"enum":["in-review","blocked","done","refinement"],
						"description":"New status for the task"
					},
					"task_status_description":{
						"type":"string",
						"description":"Short one-sentence summary of the outcome (shown inline in the timeline)"
					},
					"task_status_explanation":{
						"type":"string",
						"description":"Detailed markdown report of what was done, decisions made, and next steps"
					}
				},
				"required":["task_status","task_status_description","task_status_explanation"]
			}`),
		},
	}
}

func (t *FinishTaskExecution) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		TaskStatus            string `json:"task_status"`
		TaskStatusDescription string `json:"task_status_description"`
		TaskStatusExplanation string `json:"task_status_explanation"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := t.onFinish(ctx, p.TaskStatus, p.TaskStatusDescription, p.TaskStatusExplanation); err != nil {
		return "", fmt.Errorf("finish_task_execution: %w", err)
	}
	return fmt.Sprintf("Task status set to %q and result recorded.", p.TaskStatus), nil
}
