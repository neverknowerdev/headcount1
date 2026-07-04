package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// AskArtifact answers a question about one artifact's content without loading
// the content into the calling agent's context: the engine runs a separate
// one-shot LLM call that reads the artifact and returns a short answer.
type AskArtifact struct {
	fn func(ctx context.Context, filename, question string) (string, error)
}

func NewAskArtifact(fn func(ctx context.Context, filename, question string) (string, error)) *AskArtifact {
	return &AskArtifact{fn: fn}
}

func (t *AskArtifact) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "ask_artifact",
			Description: "Ask a question about an artifact's content and get a short answer, without reading the artifact " +
				"into your own context (a separate lightweight reader answers from the document). " +
				"Use it to verify deliverables: \"Does it contain a Roadmap section?\", \"Which files does it say were changed?\". " +
				"Ask one specific question at a time; use list_artifacts first to see what exists.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"filename":{
						"type":"string",
						"description":"Artifact filename, e.g. \"report.md\""
					},
					"question":{
						"type":"string",
						"description":"One specific question about the artifact's content"
					}
				},
				"required":["filename","question"]
			}`),
		},
	}
}

func (t *AskArtifact) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Filename string `json:"filename"`
		Question string `json:"question"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("ask_artifact: %w", err)
	}
	if p.Filename == "" {
		return "", fmt.Errorf("filename is required")
	}
	if p.Question == "" {
		return "", fmt.Errorf("question is required")
	}
	return t.fn(ctx, p.Filename, p.Question)
}
