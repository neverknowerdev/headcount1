package aicli

import (
	"context"
	"encoding/json"
	"fmt"
)

// UpdateTaskStatusTool builds the special update_task_status tool that is
// injected by the NativeEngine. The onUpdate callback is called synchronously
// with the requested status string; it should update the DB and broadcast an
// event to connected clients.
func UpdateTaskStatusTool(onUpdate func(ctx context.Context, status string) error) Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
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
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if err := onUpdate(ctx, p.Status); err != nil {
				return "", fmt.Errorf("update_task_status: %w", err)
			}
			return fmt.Sprintf("Task status updated to %q", p.Status), nil
		},
	}
}
