package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"agent-orchestrator/engine/aicli"
)

// ReadFile reads a file from within the workspace sandbox.
type ReadFile struct {
	workspacePath string
}

// NewReadFile creates a ReadFile tool sandboxed to workspacePath.
func NewReadFile(workspacePath string) *ReadFile {
	return &ReadFile{workspacePath: workspacePath}
}

func (t *ReadFile) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "read_file",
			Description: "Read a file inside the workspace. Returns the file content as text.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":{"type":"string","description":"Path relative to the workspace root (e.g. \"src/main.go\")"}
				},
				"required":["path"]
			}`),
		},
	}
}

func (t *ReadFile) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	resolved, err := resolvePath(t.workspacePath, p.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	content := string(data)
	if len(content) > 100_000 {
		content = content[:100_000] + "\n... (truncated)"
	}
	return content, nil
}
