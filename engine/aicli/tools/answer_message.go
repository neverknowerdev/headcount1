package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// AnswerMessage is intentionally a normal tool implementation whose registry
// lifetime is controlled by the engine. It should only be registered when the
// current run has pending inbound messages.
type AnswerMessage struct {
	fn func(context.Context, int64, string) (string, error)
}

func NewAnswerMessage(fn func(context.Context, int64, string) (string, error)) *AnswerMessage {
	return &AnswerMessage{fn: fn}
}

func (t *AnswerMessage) Def() aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
		Name:        string(aicli.ToolAnswerMessage),
		Description: "Answer one pending inbound message. Use the exact message_id shown in Incoming messages; each message can be answered once.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"message_id":{"type":"integer"},"answer":{"type":"string"}},"required":["message_id","answer"]}`),
	}}
}

func (t *AnswerMessage) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		MessageID int64  `json:"message_id"`
		Answer    string `json:"answer"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("answer_message: %w", err)
	}
	if p.MessageID <= 0 {
		return "", fmt.Errorf("message_id must be positive")
	}
	if p.Answer == "" {
		return "", fmt.Errorf("answer is required")
	}
	return t.fn(ctx, p.MessageID, p.Answer)
}
