package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// UpdateTaskStatus signals task progress back to the orchestrator.
type UpdateTaskStatus struct {
	onUpdate func(ctx context.Context, status string) error
}

// NewUpdateTaskStatus creates an UpdateTaskStatus tool that calls onUpdate when
// the LLM invokes it. onUpdate should update the DB and broadcast an event.
func NewUpdateTaskStatus(onUpdate func(ctx context.Context, status string) error) *UpdateTaskStatus {
	return &UpdateTaskStatus{onUpdate: onUpdate}
}

func (t *UpdateTaskStatus) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "update_task_status",
			Description: "Update the status of the current task. Call this to signal progress: use 'in-progress' when starting, 'in-review' when done, 'blocked' when stuck, 'refinement' when you have questions.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"status":{
						"type":"string",
						"enum":["refinement","in-progress","blocked","in-review"],
						"description":"The new task status"
					}
				},
				"required":["status"]
			}`),
		},
	}
}

func (t *UpdateTaskStatus) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := t.onUpdate(ctx, p.Status); err != nil {
		return "", fmt.Errorf("update_task_status: %w", err)
	}
	return fmt.Sprintf("Task status updated to %q", p.Status), nil
}
