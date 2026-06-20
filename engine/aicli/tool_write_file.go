package aicli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func writeFileTool(workspacePath string) Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
				Name:        "write_file",
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
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			resolved, err := resolvePath(workspacePath, p.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(resolved), 0755); err != nil {
				return "", fmt.Errorf("write_file: mkdir: %w", err)
			}
			if err := os.WriteFile(resolved, []byte(p.Content), 0644); err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
		},
	}
}
