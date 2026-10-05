package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

type CreateSubtaskParams struct {
	Title            string  `json:"title"`
	Description      string  `json:"description"`
	DependsOnTaskIDs []int32 `json:"depends_on_task_ids"`
	RelatedToTaskIDs []int32 `json:"related_to_task_ids"`
}

type CreateSubtask struct {
	fn func(context.Context, CreateSubtaskParams) (string, error)
}

func NewCreateSubtask(fn func(context.Context, CreateSubtaskParams) (string, error)) *CreateSubtask {
	return &CreateSubtask{fn: fn}
}

func (t *CreateSubtask) Def() aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
		Name:        string(aicli.ToolCreateSubtask),
		Description: "Create a durable child task for later orchestrator scheduling. This records planning only; it does not assign an agent, start a run, or wait.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"description":{"type":"string"},"depends_on_task_ids":{"type":"array","items":{"type":"integer"}},"related_to_task_ids":{"type":"array","items":{"type":"integer"}}},"required":["title","description"]}`),
	}}
}

func (t *CreateSubtask) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p CreateSubtaskParams
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("create_subtask: %w", err)
	}
	if p.Title == "" || p.Description == "" {
		return "", fmt.Errorf("title and description are required")
	}
	return t.fn(ctx, p)
}
