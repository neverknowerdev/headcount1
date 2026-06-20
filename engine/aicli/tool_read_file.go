package aicli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

func readFileTool(workspacePath string) Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
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
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			resolved, err := resolvePath(workspacePath, p.Path)
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
		},
	}
}
