package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"agent-orchestrator/engine/aicli"
)

// absolutePathRe matches absolute filesystem paths that a sandboxed subtask
// cannot access (macOS/Linux home paths, generic absolute unix paths with 2+
// segments, and Windows drive paths).
var absolutePathRe = regexp.MustCompile(`(?m)(/Users/[\w.-]+|/home/[\w.-]+|[A-Za-z]:\\\\?[\w.-]+)`)

// CreateSubtask delegates work to a specialist agent by creating a child task
// and WAITING for it to finish. The result message includes the subtask's
// final status, run ID, result summary, and any artifacts it produced.
type CreateSubtask struct {
	fn         func(ctx context.Context, title, description, agentName string) (string, error)
	agentNames []string
}

func NewCreateSubtask(fn func(ctx context.Context, title, description, agentName string) (string, error), agentNames []string) *CreateSubtask {
	return &CreateSubtask{fn: fn, agentNames: agentNames}
}

func (t *CreateSubtask) Def() aicli.ToolDef {
	agentNameProp := map[string]interface{}{
		"type":        "string",
		"description": "Name of the specialist agent config to use",
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
				"type": "string",
				"description": "Scoped instruction for the specialist: the goal, the inputs (name existing artifacts to read via " +
					"read_artifact and relevant run IDs for expand_run_result), the expected output (artifact filename(s) to write), " +
					"and constraints. Do NOT include absolute filesystem paths — the subtask is sandboxed to its own workspace and " +
					"reaches the codebase through the codegraph tools and workspace-relative paths.",
			},
			"agent_name": agentNameProp,
		},
		"required": []string{"title", "description", "agent_name"},
	})
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "create_subtask",
			Description: "Delegate a scoped piece of work to a specialist agent and WAIT for its result. The returned message " +
				"contains the subtask's final status, its run ID (use expand_run_result for the full detailed handoff), and the " +
				"artifacts it wrote (read them with read_artifact). Only one subtask can run at a time.",
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
	// Fail fast on instructions the sandboxed subtask cannot follow: absolute
	// paths outside its workspace waste an entire delegated run.
	if m := absolutePathRe.FindString(p.Description); m != "" {
		return "", fmt.Errorf("description references the absolute path %q, which the sandboxed subtask cannot access — "+
			"rewrite the instruction to reference artifacts (read_artifact), the codegraph project, or workspace-relative paths", m)
	}
	return t.fn(ctx, p.Title, p.Description, p.AgentName)
}
