package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// SubtaskParams holds the arguments provided by the LLM when calling
// create_subtask.
type SubtaskParams struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	AgentName   string `json:"agent_name"`
}

// CreateSubtaskFunc is the callback the engine supplies to perform the actual
// DB write and task dispatch. It returns the created task ID.
type CreateSubtaskFunc func(ctx context.Context, p SubtaskParams) (int32, error)

type createSubtaskTool struct {
	fn CreateSubtaskFunc
}

// NewCreateSubtask returns a tool that creates a subtask and delegates its
// execution to a named agent. fn is called to perform the DB write and
// to enqueue the new task for processing.
func NewCreateSubtask(fn CreateSubtaskFunc) aicli.Tool {
	return &createSubtaskTool{fn: fn}
}

func (t *createSubtaskTool) Def() aicli.ToolDef {
	params := json.RawMessage(`{
		"type": "object",
		"properties": {
			"title": {
				"type": "string",
				"description": "Short title for the subtask"
			},
			"description": {
				"type": "string",
				"description": "Detailed description of what the subtask should accomplish"
			},
			"agent_name": {
				"type": "string",
				"description": "Name of the agent config to use (e.g. Programmer, QA, Researcher)"
			}
		},
		"required": ["title", "description", "agent_name"]
	}`)
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "create_subtask",
			Description: "Create a subtask and delegate its execution to a specialist agent. Only one subtask can run at a time — this call fails if a subtask is already running.",
			Parameters:  params,
		},
	}
}

func (t *createSubtaskTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p SubtaskParams
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid create_subtask args: %w", err)
	}
	if p.Title == "" {
		return "", fmt.Errorf("title is required")
	}
	if p.AgentName == "" {
		return "", fmt.Errorf("agent_name is required")
	}
	taskID, err := t.fn(ctx, p)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Subtask %d created and queued for execution by %s agent.", taskID, p.AgentName), nil
}
