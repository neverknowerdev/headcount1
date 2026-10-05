package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"agent-orchestrator/engine/aicli"
)

// WriteFile writes a file inside the workspace sandbox.
type WriteFile struct {
	workspacePath string
}

// NewWriteFile creates a WriteFile tool sandboxed to workspacePath.
func NewWriteFile(workspacePath string) *WriteFile {
	return &WriteFile{workspacePath: workspacePath}
}

func (t *WriteFile) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        string(aicli.ToolWrite),
			Description: "Write content to a file inside the workspace. Creates parent directories as needed.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":{"type":"string","description":"Path relative to the workspace root (e.g. \"output/result.txt\")"},
					"content":{"type":"string","description":"Content to write to the file"}
				},
				"required":["path","content"]
			}`),
		},
	}
}

func (t *WriteFile) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	resolved, err := resolvePath(t.workspacePath, p.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0755); err != nil {
		return "", fmt.Errorf("write: mkdir: %w", err)
	}
	if err := os.WriteFile(resolved, []byte(p.Content), 0644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
}
