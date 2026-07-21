package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-orchestrator/engine/aicli"
)

// ListDir lists files inside the workspace sandbox.
type ListDir struct {
	workspacePath string
	readOnlyDirs  []string
}

// NewListDir creates a ListDir tool sandboxed to workspacePath plus optional
// read-only roots.
func NewListDir(workspacePath string, readOnlyDirs ...string) *ListDir {
	return &ListDir{workspacePath: workspacePath, readOnlyDirs: readOnlyDirs}
}

func (t *ListDir) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "ls",
			Description: "List files inside the workspace. Returns a line-per-entry listing.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":{"type":"string","description":"Directory path relative to the workspace root (use \".\" for the root)"},
					"recursive":{"type":"boolean","description":"Whether to list recursively (default false)"}
				},
				"required":["path"]
			}`),
		},
	}
}

func (t *ListDir) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	resolved, err := resolveReadPath(t.workspacePath, t.readOnlyDirs, p.Path)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	if p.Recursive {
		err := filepath.WalkDir(resolved, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(resolved, path)
			if d.IsDir() {
				sb.WriteString(rel + "/\n")
			} else {
				sb.WriteString(rel + "\n")
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("ls: %w", err)
		}
	} else {
		entries, err := os.ReadDir(resolved)
		if err != nil {
			return "", fmt.Errorf("ls: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				sb.WriteString(e.Name() + "/\n")
			} else {
				sb.WriteString(e.Name() + "\n")
			}
		}
	}
	if sb.Len() == 0 {
		return "(empty directory)", nil
	}
	return sb.String(), nil
}
