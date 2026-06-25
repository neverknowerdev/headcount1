package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/engine/aicli"
)

// WriteArtifactFile lets the agent publish a markdown document as a named
// output artifact attached to the current task.
type WriteArtifactFile struct {
	onWrite func(ctx context.Context, filename, content string) error
}

func NewWriteArtifactFile(onWrite func(ctx context.Context, filename, content string) error) *WriteArtifactFile {
	return &WriteArtifactFile{onWrite: onWrite}
}

func (t *WriteArtifactFile) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "write_artifact_file",
			Description: "Write a markdown (.md) output artifact for the current task. " +
				"Use this to produce structured deliverables: plans, reports, documentation, specs, etc. " +
				"Artifacts are saved to the project artifacts directory and displayed in the task UI. " +
				"Only .md files are accepted.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"filename":{
						"type":"string",
						"description":"File name ending in .md, e.g. \"plan.md\" or \"report.md\""
					},
					"content":{
						"type":"string",
						"description":"Full markdown content of the artifact"
					}
				},
				"required":["filename","content"]
			}`),
		},
	}
}

func (t *WriteArtifactFile) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(p.Filename), ".md") {
		return "", fmt.Errorf("write_artifact_file: only .md files are allowed, got %q", p.Filename)
	}
	if strings.ContainsAny(p.Filename, "/\\") {
		return "", fmt.Errorf("write_artifact_file: filename must not contain path separators")
	}
	if err := t.onWrite(ctx, p.Filename, p.Content); err != nil {
		return "", fmt.Errorf("write_artifact_file: %w", err)
	}
	return fmt.Sprintf("Artifact %q written.", p.Filename), nil
}
