package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// AskCEO is an orchestrator-only consultation request. The engine supplies
// task context through the normal CEO session bootstrap; this payload is
// deliberately limited to the task ID and the caller's message.
type AskCEO struct {
	fn func(context.Context, int32, string) (string, error)
}

func NewAskCEO(fn func(context.Context, int32, string) (string, error)) *AskCEO {
	return &AskCEO{fn: fn}
}

func (t *AskCEO) Def() aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
		Name:        string(aicli.ToolAskCEO),
		Description: "Start a CEO consultation for the current task and wait for the correlated answer.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"integer"},"message":{"type":"string"}},"required":["task_id","message"]}`),
	}}
}

func (t *AskCEO) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		TaskID  int32  `json:"task_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("ask_ceo: %w", err)
	}
	if p.TaskID <= 0 || p.Message == "" {
		return "", fmt.Errorf("task_id and message are required")
	}
	if t.fn == nil {
		return "", fmt.Errorf("ask_ceo is unavailable")
	}
	return t.fn(ctx, p.TaskID, p.Message)
}
