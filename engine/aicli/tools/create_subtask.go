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
// Subtask instructions must reference artifacts, the codegraph project, or
// workspace-relative paths instead.
var absolutePathRe = regexp.MustCompile(`(?m)(/Users/[\w.-]+|/home/[\w.-]+|[A-Za-z]:\\\\?[\w.-]+)`)

// CreateSubtask delegates a scoped piece of work to a sub-agent by creating a
// child task and running it as a nested execution session. The call blocks
// until the session either finishes (returning its final result and produced
// artifacts) or asks the task owner a question (returning the question, to be
// answered via answer_subtask_question).
type CreateSubtask struct {
	fn         func(ctx context.Context, title, description, agentName string) (string, error)
	agentNames []string
}

// NewCreateSubtask wraps the engine callback that creates the subtask, runs
// the nested session, and returns the session's result (or a pending question
// from the sub-agent).
func NewCreateSubtask(fn func(ctx context.Context, title, description, agentName string) (string, error), agentNames []string) *CreateSubtask {
	return &CreateSubtask{fn: fn, agentNames: agentNames}
}

func (t *CreateSubtask) Def() aicli.ToolDef {
	agentNameProp := map[string]interface{}{
		"type":        "string",
		"description": "Name of the sub-agent to assign the subtask to",
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
				"description": "Detailed, scoped instructions for the sub-agent: what to do, relevant context, and what the expected result looks like",
			},
			"agent_name": agentNameProp,
		},
		"required": []string{"title", "description", "agent_name"},
	})
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: string(ToolCreateSubtask),
			Description: "Create a subtask and assign it to a sub-agent. Runs the subtask as a nested session and waits: " +
				"returns the sub-agent's final result and produced artifacts when it finishes, or the sub-agent's question " +
				"(answer it with answer_subtask_question) when it needs input from you. Only one subtask can run at a time.",
			Parameters: json.RawMessage(schema),
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
	// Fail fast on instructions the sandboxed session cannot follow: absolute
	// paths outside its workspace waste an entire delegated run.
	if m := absolutePathRe.FindString(p.Description); m != "" {
		return "", fmt.Errorf("description references the absolute path %q, which the sandboxed session cannot access — "+
			"rewrite the instruction to reference artifacts (read_artifact), the codegraph project, or workspace-relative paths", m)
	}
	return t.fn(ctx, p.Title, p.Description, p.AgentName)
}
