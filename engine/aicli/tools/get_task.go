package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

type GetTask struct {
	fn func(context.Context, string) (string, error)
}

func NewGetTask(fn func(context.Context, string) (string, error)) *GetTask { return &GetTask{fn: fn} }

func (t *GetTask) Def() aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
		Name:        string(aicli.ToolGetTask),
		Description: "Read the bounded operational view of a task, including specification, relations, comments, runs, and Git/PR state.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string","description":"Numeric task ID or task reference"}},"required":["task_id"]}`),
	}}
}

func (t *GetTask) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("get_task: %w", err)
	}
	if p.TaskID == "" {
		return "", fmt.Errorf("task_id is required")
	}
	return t.fn(ctx, p.TaskID)
}
