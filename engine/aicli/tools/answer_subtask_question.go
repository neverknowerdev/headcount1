package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// AnswerSubtaskQuestion delivers the task owner's answer to a sub-agent that
// is paused on ask_task_owner, then blocks again until the subtask either
// finishes or asks another question. It is only useful while a subtask has a
// pending question (create_subtask returned one).
type AnswerSubtaskQuestion struct {
	fn func(ctx context.Context, subtaskID int32, answer string) (string, error)
}

func NewAnswerSubtaskQuestion(fn func(ctx context.Context, subtaskID int32, answer string) (string, error)) *AnswerSubtaskQuestion {
	return &AnswerSubtaskQuestion{fn: fn}
}

func (t *AnswerSubtaskQuestion) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: string(aicli.ToolAnswerSubtaskQuestion),
			Description: "Answer a pending question from a subtask's sub-agent (asked via ask_task_owner). " +
				"The sub-agent resumes with your answer; the call then waits and returns the subtask's final result " +
				"or its next question. Only valid while a subtask has a pending question.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"subtask_id":{
						"type":"integer",
						"description":"ID of the subtask whose question you are answering. Optional when exactly one question is pending."
					},
					"answer":{
						"type":"string",
						"description":"Your answer to the sub-agent's question."
					}
				},
				"required":["answer"]
			}`),
		},
	}
}

func (t *AnswerSubtaskQuestion) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SubtaskID int32  `json:"subtask_id"`
		Answer    string `json:"answer"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("answer_subtask_question: %w", err)
	}
	if p.Answer == "" {
		return "", fmt.Errorf("answer is required")
	}
	return t.fn(ctx, p.SubtaskID, p.Answer)
}
