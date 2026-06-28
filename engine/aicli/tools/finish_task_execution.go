package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// FinishTask is called by the agent at the end of every run to record the
// outcome, update the task status, and leave a visible one-line summary.
type FinishTask struct {
	onFinish func(ctx context.Context, status, finishStatus string) error
}

func NewFinishTask(onFinish func(ctx context.Context, status, finishStatus string) error) *FinishTask {
	return &FinishTask{onFinish: onFinish}
}

func (t *FinishTask) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "finish_task",
			Description: "MUST be called at the end of every run to record the outcome and update the task status. " +
				"Use 'in-review' when the work is done and ready for human review. " +
				"Use 'blocked' when you are stuck and need user input. " +
				"Use 'done' when the task is fully complete and no review is needed. " +
				"Use 'refinement' when you need clarification before you can proceed. " +
				"finish_status is a short one-sentence summary of the outcome shown to the user.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"task_status":{
						"type":"string",
						"enum":["in-review","blocked","done","refinement"],
						"description":"New status for the task"
					},
					"finish_status":{
						"type":"string",
						"description":"Short one-sentence summary of the outcome (shown in the activity timeline)"
					}
				},
				"required":["task_status","finish_status"]
			}`),
		},
	}
}

func (t *FinishTask) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		TaskStatus   string `json:"task_status"`
		FinishStatus string `json:"finish_status"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := t.onFinish(ctx, p.TaskStatus, p.FinishStatus); err != nil {
		return "", fmt.Errorf("finish_task: %w", err)
	}
	return fmt.Sprintf("Task status set to %q.", p.TaskStatus), nil
}
