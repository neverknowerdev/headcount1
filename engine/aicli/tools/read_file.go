package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"agent-orchestrator/engine/aicli"
)

// ReadFile reads a file from within the workspace sandbox. Extra read-only
// roots (parent task workdir, artifacts dir) are readable but never writable.
type ReadFile struct {
	workspacePath string
	readOnlyDirs  []string
}

// NewReadFile creates a ReadFile tool sandboxed to workspacePath plus
// optional read-only roots.
func NewReadFile(workspacePath string, readOnlyDirs ...string) *ReadFile {
	return &ReadFile{workspacePath: workspacePath, readOnlyDirs: readOnlyDirs}
}

func (t *ReadFile) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        string(ToolRead),
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
	resolved, err := resolveReadPath(t.workspacePath, t.readOnlyDirs, p.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	content := string(data)
	if len(content) > 100_000 {
		content = content[:100_000] + "\n... (truncated)"
	}
	return content, nil
}
