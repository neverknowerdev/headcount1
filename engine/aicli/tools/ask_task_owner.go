package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// AskTaskOwner lets a delegated sub-agent ask the agent that created its
// subtask a question. The call blocks until the task owner replies (via
// answer_subtask_question in the owner's session), and the reply is returned
// as the tool result.
type AskTaskOwner struct {
	fn func(ctx context.Context, question string) (string, error)
}

func NewAskTaskOwner(fn func(ctx context.Context, question string) (string, error)) *AskTaskOwner {
	return &AskTaskOwner{fn: fn}
}

func (t *AskTaskOwner) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "ask_task_owner",
			Description: "Ask the agent that created your subtask (your task owner) a question and wait for the answer. " +
				"Use it when your instructions are ambiguous or you need a decision that belongs to the owner. " +
				"Your session pauses until the owner replies.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"question":{
						"type":"string",
						"description":"The question for the task owner. Be specific; include the options you are choosing between."
					}
				},
				"required":["question"]
			}`),
		},
	}
}

func (t *AskTaskOwner) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("ask_task_owner: %w", err)
	}
	if p.Question == "" {
		return "", fmt.Errorf("question is required")
	}
	return t.fn(ctx, p.Question)
}
