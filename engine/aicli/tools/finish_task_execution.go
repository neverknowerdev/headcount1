package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// FinishTask is called by the agent at the end of every run to record the
// outcome, update the task status, and leave a visible one-line summary plus
// a detailed result that is handed back to the task owner when the session
// ran as a delegated subtask.
type FinishTask struct {
	delegated bool
	onFinish  func(ctx context.Context, result FinishTaskResult) error
}

type FinishTaskResult struct {
	Status                 string
	FinishStatus           string
	ResultDetails          string
	PullRequestTitle       string
	PullRequestDescription string
}

// NewFinishTask builds the finish_task tool. delegated marks sessions that
// run a subtask created by another agent: their result is reviewed by the
// task owner automatically, so "done" is the normal completion status and
// "in-review" is reserved for work a human specifically must approve.
func NewFinishTask(delegated bool, onFinish func(ctx context.Context, result FinishTaskResult) error) *FinishTask {
	return &FinishTask{delegated: delegated, onFinish: onFinish}
}

func (t *FinishTask) Def() aicli.ToolDef {
	statusGuidance := "Choose the final status yourself: " +
		"use 'done' when the work is complete and needs no human attention; " +
		"use 'in-review' when the result should be reviewed or approved by a human before it counts as finished. "
	if t.delegated {
		statusGuidance = "This session runs a subtask created by another agent, and that task owner receives your full result automatically. " +
			"Use 'done' when your work is complete — do NOT use 'in-review' unless a human specifically must approve something. "
	}
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: string(aicli.ToolFinishTask),
			Description: "MUST be called at the end of every run to record the outcome and update the task status. " +
				statusGuidance +
				"Use 'blocked' when you are stuck and need human input — including when you cannot actually verify or complete " +
				"what was asked (never report success you did not verify). " +
				"finish_status is a short one-sentence summary shown to the user. " +
				"result_details is the full handoff for whoever consumes your work: key findings, decisions, artifact filenames, " +
				"file paths touched, caveats and assumptions. It is returned to your task owner when your session completes — make it complete.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"task_status":{
						"type":"string",
						"enum":["done","in-review","blocked"],
						"description":"New status for the task"
					},
					"finish_status":{
						"type":"string",
						"description":"Short one-sentence summary of the outcome (shown in the activity timeline)"
					},
					"result_details":{
						"type":"string",
						"description":"Detailed handoff: findings, decisions, artifact filenames, caveats. Returned to your task owner when the session completes."
					},
					"pull_request_title":{
						"type":"string",
						"description":"For code changes, a concise pull request title describing the implementation."
					},
					"pull_request_description":{
						"type":"string",
						"description":"For code changes, a useful pull request description covering the outcome, key changes, and verification."
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
		PRTitle       string `json:"pull_request_title"`
		PRDescription string `json:"pull_request_description"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	result := FinishTaskResult{
		Status: p.TaskStatus, FinishStatus: p.FinishStatus, ResultDetails: p.ResultDetails,
		PullRequestTitle: p.PRTitle, PullRequestDescription: p.PRDescription,
	}
	if err := t.onFinish(ctx, result); err != nil {
		return "", fmt.Errorf("finish_task: %w", err)
	}
	return fmt.Sprintf("Task status set to %q.", p.TaskStatus), nil
}
