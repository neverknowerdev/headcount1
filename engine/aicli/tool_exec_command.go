package aicli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func execCommandTool(workspacePath string) Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
				Name:        "exec_command",
				Description: "Execute a shell command inside the workspace. Runs with the workspace as the working directory. Only use relative paths — paths outside the workspace are rejected.",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"command":{"type":"string","description":"Shell command to run using relative paths (e.g. \"go test ./...\", \"ls src/\")"}
					},
					"required":["command"]
				}`),
			},
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if err := validateCommandPaths(workspacePath, p.Command); err != nil {
				return "", err
			}
			// 60-second hard cap so a misbehaving command can't stall the run forever.
			cmdCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			cmd := exec.CommandContext(cmdCtx, "sh", "-c", p.Command)
			cmd.Dir = workspacePath
			output, err := cmd.CombinedOutput()
			result := string(output)
			if len(result) > 50_000 {
				result = result[:50_000] + "\n... (truncated)"
			}
			if err != nil {
				return fmt.Sprintf("exit error: %v\n%s", err, result), nil
			}
			return result, nil
		},
	}
}

// validateCommandPaths rejects shell commands that reference paths outside the
// workspace. It scans whitespace-separated tokens and checks:
//   - absolute paths (e.g. /etc/passwd, /tmp/evil)
//   - relative traversals (e.g. .., ../foo, a/../../b)
//
// Plain words (command names, flags, simple filenames) are skipped.
// This covers the common cases; it does not prevent every possible bypass
// (shell substitution, env vars, etc.) but stops straightforward escapes.
func validateCommandPaths(workspacePath, command string) error {
	workspace := filepath.Clean(workspacePath)
	for _, token := range strings.Fields(command) {
		token = strings.Trim(token, `"'`)
		if token == "" {
			continue
		}
		var abs string
		switch {
		case filepath.IsAbs(token):
			abs = filepath.Clean(token)
		case looksLikePath(token):
			abs = filepath.Clean(filepath.Join(workspacePath, token))
		default:
			continue
		}
		if abs != workspace && !strings.HasPrefix(abs, workspace+string(filepath.Separator)) {
			return fmt.Errorf("path %q escapes the workspace", token)
		}
	}
	return nil
}

// looksLikePath returns true when a relative token should be resolved and
// sandbox-checked. Matches tokens that start with ./ or ../, equal "..",
// or contain ".." as a standalone path component (e.g. "a/../../b").
func looksLikePath(token string) bool {
	if token == ".." || strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") {
		return true
	}
	for _, part := range strings.Split(token, string(filepath.Separator)) {
		if part == ".." {
			return true
		}
	}
	return false
}
