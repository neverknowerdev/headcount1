package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// DelegateTask delegates a scoped piece of work to a specialist agent by
// creating a child task and running it as a nested execution session. The
// call blocks until the session finishes and returns its result, so the
// delegating agent can react to the outcome in its next turn.
type DelegateTask struct {
	fn         func(ctx context.Context, title, description, agentName string) (string, error)
	agentNames []string
}

// NewDelegateTask wraps the engine callback that creates the subtask, runs the
// nested session synchronously, and returns the session's result summary.
func NewDelegateTask(fn func(ctx context.Context, title, description, agentName string) (string, error), agentNames []string) *DelegateTask {
	return &DelegateTask{fn: fn, agentNames: agentNames}
}

func (t *DelegateTask) Def() aicli.ToolDef {
	agentNameProp := map[string]interface{}{
		"type":        "string",
		"description": "Name of the specialist agent to delegate to",
	}
	if len(t.agentNames) > 0 {
		agentNameProp["enum"] = t.agentNames
	}
	schema, _ := json.Marshal(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Short title for the delegated subtask",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Detailed, scoped instructions for the specialist: what to do, relevant context, and what the expected result looks like",
			},
			"agent_name": agentNameProp,
		},
		"required": []string{"title", "description", "agent_name"},
	})
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "delegate_task",
			Description: "Delegate a scoped piece of work to a specialist agent. Creates a subtask, runs it as a nested session, " +
				"waits for completion, and returns the specialist's result. Only one delegation can run at a time.",
			Parameters: json.RawMessage(schema),
		},
	}
}

func (t *DelegateTask) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		AgentName   string `json:"agent_name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("delegate_task: %w", err)
	}
	if p.Title == "" {
		return "", fmt.Errorf("title is required")
	}
	if p.AgentName == "" {
		return "", fmt.Errorf("agent_name is required")
	}
	return t.fn(ctx, p.Title, p.Description, p.AgentName)
}
