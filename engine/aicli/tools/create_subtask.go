package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// CreateSubtask delegates work to a specialist agent by creating a child task.
type CreateSubtask struct {
	fn         func(ctx context.Context, title, description, agentName string) (int32, error)
	agentNames []string
}

func NewCreateSubtask(fn func(ctx context.Context, title, description, agentName string) (int32, error), agentNames []string) *CreateSubtask {
	return &CreateSubtask{fn: fn, agentNames: agentNames}
}

func (t *CreateSubtask) Def() aicli.ToolDef {
	agentNameProp := map[string]interface{}{
		"type":        "string",
		"description": "Name of the agent config to use",
	}
	if len(t.agentNames) > 0 {
		agentNameProp["enum"] = t.agentNames
	}
	schema, _ := json.Marshal(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Short title for the subtask",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Detailed description of what the subtask should accomplish",
			},
			"agent_name": agentNameProp,
		},
		"required": []string{"title", "description", "agent_name"},
	})
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "create_subtask",
			Description: "Create a subtask and delegate its execution to a specialist agent. Only one subtask can run at a time — this call fails if a subtask is already running.",
			Parameters:  json.RawMessage(schema),
		},
	}
}

func (t *CreateSubtask) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		AgentName   string `json:"agent_name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("create_subtask: %w", err)
	}
	if p.Title == "" {
		return "", fmt.Errorf("title is required")
	}
	if p.AgentName == "" {
		return "", fmt.Errorf("agent_name is required")
	}
	taskID, err := t.fn(ctx, p.Title, p.Description, p.AgentName)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Subtask %d created and queued for execution by %s agent.", taskID, p.AgentName), nil
}
