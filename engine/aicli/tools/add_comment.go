package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// AddComment lets the agent leave a structured comment on the task.
type AddComment struct {
	onCreate func(ctx context.Context, commentType, content string) error
}

func NewAddComment(onCreate func(ctx context.Context, commentType, content string) error) *AddComment {
	return &AddComment{onCreate: onCreate}
}

func (t *AddComment) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "add_comment",
			Description: "Leave a comment on the task visible to the user. " +
				"Use comment_type='ask_user' when you need clarification or input from the user. " +
				"Use comment_type='comment' for general progress updates or observations. " +
				"Use comment_type='task_done' only when calling this instead of finish_task_execution.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"comment_type":{
						"type":"string",
						"enum":["task_done","ask_user","comment"],
						"description":"Category of the comment"
					},
					"content":{
						"type":"string",
						"description":"The comment text to display to the user"
					}
				},
				"required":["comment_type","content"]
			}`),
		},
	}
}

func (t *AddComment) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		CommentType string `json:"comment_type"`
		Content     string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := t.onCreate(ctx, p.CommentType, p.Content); err != nil {
		return "", fmt.Errorf("add_comment: %w", err)
	}
	return "Comment added.", nil
}
