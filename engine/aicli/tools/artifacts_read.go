package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// ArtifactInfo is a compact listing entry for one artifact.
type ArtifactInfo struct {
	ID        int32  `json:"id"`
	Filename  string `json:"filename"`
	SizeBytes int    `json:"size_bytes"`
	WrittenBy string `json:"written_by"` // "run #N (Agent)" provenance
	UpdatedAt string `json:"updated_at"`
}

// ListArtifacts lists all artifacts of the current task tree so agents can
// discover deliverables produced by other agents (parent task or sibling
// subtasks) without guessing filenames or run IDs.
type ListArtifacts struct {
	fn func(ctx context.Context) ([]ArtifactInfo, error)
}

func NewListArtifacts(fn func(ctx context.Context) ([]ArtifactInfo, error)) *ListArtifacts {
	return &ListArtifacts{fn: fn}
}

func (t *ListArtifacts) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "list_artifacts",
			Description: "List all artifacts (deliverables) of this task and its subtasks: filename, size, and which run/agent wrote it. " +
				"Use this to find deliverables produced by other agents before reading them with read_artifact.",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}
}

func (t *ListArtifacts) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	infos, err := t.fn(ctx)
	if err != nil {
		return "", fmt.Errorf("list_artifacts: %w", err)
	}
	if len(infos) == 0 {
		return "No artifacts exist yet for this task.", nil
	}
	out := fmt.Sprintf("%d artifact(s):\n", len(infos))
	for _, a := range infos {
		out += fmt.Sprintf("- %q (%d bytes, written by %s, updated %s)\n", a.Filename, a.SizeBytes, a.WrittenBy, a.UpdatedAt)
	}
	out += "Read any of them with read_artifact."
	return out, nil
}

// ReadArtifact returns the full content of one artifact by filename.
type ReadArtifact struct {
	fn func(ctx context.Context, filename string) (string, error)
}

func NewReadArtifact(fn func(ctx context.Context, filename string) (string, error)) *ReadArtifact {
	return &ReadArtifact{fn: fn}
}

func (t *ReadArtifact) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "read_artifact",
			Description: "Read the full content of an artifact (deliverable) of this task tree by filename. " +
				"Use list_artifacts first to see what exists. This is the ONLY reliable way to read deliverables " +
				"produced by other agents — artifacts are stored outside the workspace filesystem.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"filename":{"type":"string","description":"Artifact filename, e.g. \"report.md\""}},"required":["filename"]}`),
		},
	}
}

func (t *ReadArtifact) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("read_artifact: %w", err)
	}
	if p.Filename == "" {
		return "", fmt.Errorf("read_artifact: filename is required")
	}
	content, err := t.fn(ctx, p.Filename)
	if err != nil {
		return "", fmt.Errorf("read_artifact: %w", err)
	}
	return content, nil
}
