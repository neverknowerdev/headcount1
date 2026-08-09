package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// AskHuman posts a question to the task's activity feed and blocks until the
// user replies with a comment. The reply text is returned as the tool result,
// so the agent can continue the same run with the user's answer in context.
type AskHuman struct {
	fn func(ctx context.Context, question string) (string, error)
}

func NewAskHuman(fn func(ctx context.Context, question string) (string, error)) *AskHuman {
	return &AskHuman{fn: fn}
}

func (t *AskHuman) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: string(ToolAskHuman),
			Description: "Ask the user a question and wait for their answer. Use only when the gap cannot be filled by " +
				"research or delegation (business intent, preferences, approvals). The run pauses until the user replies.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"question":{
						"type":"string",
						"description":"The question to ask the user. Be specific and, where helpful, offer options."
					}
				},
				"required":["question"]
			}`),
		},
	}
}

func (t *AskHuman) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("ask_human: %w", err)
	}
	if p.Question == "" {
		return "", fmt.Errorf("question is required")
	}
	return t.fn(ctx, p.Question)
}
