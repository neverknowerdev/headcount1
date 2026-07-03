package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// FinishTask is called by the agent at the end of every run to record the
// outcome, update the task status, and leave a visible one-line summary plus
// a detailed result that other agents can retrieve via expand_run_result.
type FinishTask struct {
	onFinish func(ctx context.Context, status, finishStatus, resultDetails string) error
}

func NewFinishTask(onFinish func(ctx context.Context, status, finishStatus, resultDetails string) error) *FinishTask {
	return &FinishTask{onFinish: onFinish}
}

func (t *FinishTask) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "finish_task",
			Description: "MUST be called at the end of every run to record the outcome and update the task status. " +
				"Use 'in-review' when the work is done and ready for human review. " +
				"Use 'blocked' when you are stuck and need user input — including when you cannot actually verify or complete " +
				"what was asked (never report success you did not verify). " +
				"Use 'done' when the task is fully complete and no review is needed. " +
				"Use 'refinement' when you need clarification before you can proceed. " +
				"finish_status is a short one-sentence summary shown to the user. " +
				"result_details is the full handoff for whoever consumes your work: key findings, decisions, artifact filenames, " +
				"file paths touched, caveats and assumptions. Other agents read it via expand_run_result — make it complete.",
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
					},
					"result_details":{
						"type":"string",
						"description":"Detailed handoff: findings, decisions, artifact filenames, caveats. Read by other agents via expand_run_result."
					}
				},
				"required":["task_status","finish_status"]
			}`),
		},
	}
}

func (t *FinishTask) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		TaskStatus    string `json:"task_status"`
		FinishStatus  string `json:"finish_status"`
		ResultDetails string `json:"result_details"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := t.onFinish(ctx, p.TaskStatus, p.FinishStatus, p.ResultDetails); err != nil {
		return "", fmt.Errorf("finish_task: %w", err)
	}
	return fmt.Sprintf("Task status set to %q.", p.TaskStatus), nil
}
