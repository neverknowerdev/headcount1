package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"agent-orchestrator/engine/aicli"
)

// codeExtensions is a blocklist of file extensions that are not allowed as
// artifacts because they are source code or configuration files.
var codeExtensions = map[string]bool{
	// Languages
	".go": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
	".py": true, ".rb": true, ".java": true, ".kt": true, ".swift": true,
	".c": true, ".cpp": true, ".cc": true, ".h": true, ".hpp": true,
	".cs": true, ".rs": true, ".php": true, ".scala": true, ".clj": true,
	".ex": true, ".exs": true, ".elm": true, ".hs": true, ".ml": true,
	".fs": true, ".fsx": true, ".lua": true, ".r": true, ".m": true,
	".dart": true, ".vue": true, ".svelte": true, ".astro": true,
	// Shell / scripts
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".ps1": true, ".bat": true, ".cmd": true,
	// Config / data formats
	".json": true, ".yaml": true, ".yml": true, ".toml": true,
	".xml": true, ".ini": true, ".cfg": true, ".conf": true,
	".env": true, ".lock": true, ".gradle": true, ".cmake": true,
	// Query / schema
	".sql": true, ".graphql": true, ".gql": true, ".proto": true,
	// Build / tooling
	".makefile": true, ".dockerfile": true, ".gitignore": true,
	".dockerignore": true, ".editorconfig": true,
}

// WriteArtifactFile lets the agent publish output artifacts for the current task.
// Any file type is allowed except source code and config files.
type WriteArtifactFile struct {
	// onWrite persists the artifact and returns a human-readable result
	// message (e.g. noting that an existing artifact was overwritten and by
	// which run it was originally written).
	onWrite func(ctx context.Context, filename, content, description string) (string, error)
}

func NewWriteArtifactFile(onWrite func(ctx context.Context, filename, content, description string) (string, error)) *WriteArtifactFile {
	return &WriteArtifactFile{onWrite: onWrite}
}

func (t *WriteArtifactFile) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "write_artifact",
			Description: "Write an output artifact for the current task. " +
				"Use this for deliverables: reports (.md), specifications, diagrams (.svg), " +
				"images (.png, .jpg), PDFs, audio, video, or any non-code output. " +
				"Source code and config files (.go, .js, .json, .yaml, etc.) are not allowed. " +
				"Artifacts are saved to the project artifacts directory and shown in the task UI.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"filename":{
						"type":"string",
						"description":"File name with extension, e.g. \"report.md\", \"diagram.svg\", \"analysis.pdf\""
					},
					"content":{
						"type":"string",
						"description":"File content (text for .md/.svg/.html/.txt; base64-encoded for binary formats)"
					},
					"description":{
						"type":"string",
						"description":"One short sentence describing what this artifact contains (shown in artifact listings)"
					}
				},
				"required":["filename","content"]
			}`),
		},
	}
}

func (t *WriteArtifactFile) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Filename    string `json:"filename"`
		Content     string `json:"content"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.ContainsAny(p.Filename, "/\\") {
		return "", fmt.Errorf("write_artifact: filename must not contain path separators")
	}
	ext := strings.ToLower(filepath.Ext(p.Filename))
	if ext == "" {
		return "", fmt.Errorf("write_artifact: filename must have an extension")
	}
	if codeExtensions[ext] {
		return "", fmt.Errorf("write_artifact: %q is a code/config file type and cannot be an artifact; use write instead", ext)
	}
	msg, err := t.onWrite(ctx, p.Filename, p.Content, p.Description)
	if err != nil {
		return "", fmt.Errorf("write_artifact: %w", err)
	}
	if msg == "" {
		msg = fmt.Sprintf("Artifact %q written.", p.Filename)
	}
	return msg, nil
}
