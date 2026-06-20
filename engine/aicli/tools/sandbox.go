package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// resolvePath resolves path relative to workspacePath and verifies the result
// stays inside the workspace. Returns an error if the path escapes.
func resolvePath(workspacePath, path string) (string, error) {
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(filepath.Join(workspacePath, path))
	}
	workspace := filepath.Clean(workspacePath)
	if abs != workspace && !strings.HasPrefix(abs, workspace+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", path)
	}
	return abs, nil
}

// validateCommandPaths rejects shell commands that reference paths outside the
// workspace — absolute paths and relative traversals alike.
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

// looksLikePath returns true when a relative token should be sandbox-checked.
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
