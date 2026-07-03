package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"agent-orchestrator/engine/aicli"
)

// absolutePathRe matches absolute filesystem paths that a sandboxed delegated
// session cannot access (macOS/Linux home paths and Windows drive paths).
// Delegation instructions must reference artifacts, run IDs, the codegraph
// project, or workspace-relative paths instead.
var absolutePathRe = regexp.MustCompile(`(?m)(/Users/[\w.-]+|/home/[\w.-]+|[A-Za-z]:\\\\?[\w.-]+)`)

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
	// Fail fast on instructions the sandboxed session cannot follow: absolute
	// paths outside its workspace waste an entire delegated run.
	if m := absolutePathRe.FindString(p.Description); m != "" {
		return "", fmt.Errorf("description references the absolute path %q, which the sandboxed session cannot access — "+
			"rewrite the instruction to reference artifacts (read_artifact), the codegraph project, or workspace-relative paths", m)
	}
	return t.fn(ctx, p.Title, p.Description, p.AgentName)
}
