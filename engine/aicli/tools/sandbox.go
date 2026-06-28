package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolvePath resolves path relative to workspacePath and verifies the result
// stays inside the workspace. Returns an error if the path escapes.
func resolvePath(workspacePath, path string) (string, error) {
	var abs string
	switch {
	case filepath.IsAbs(path):
		abs = filepath.Clean(path)
	case path == "~":
		homeDir, _ := os.UserHomeDir()
		abs = homeDir
	case strings.HasPrefix(path, "~/"):
		homeDir, _ := os.UserHomeDir()
		abs = filepath.Clean(filepath.Join(homeDir, path[2:]))
	default:
		abs = filepath.Clean(filepath.Join(workspacePath, path))
	}
	workspace := filepath.Clean(workspacePath)
	if abs != workspace && !strings.HasPrefix(abs, workspace+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", path)
	}
	return abs, nil
}

// validateCommandPaths rejects shell commands that reference paths outside the
// workspace — absolute paths, home-dir (~) paths, and relative traversals alike.
func validateCommandPaths(workspacePath, command string) error {
	// Reject $HOME / ${HOME} variable references that could bypass path checking.
	if strings.Contains(command, "$HOME") || strings.Contains(command, "${HOME}") {
		return fmt.Errorf("command references $HOME which may escape the workspace")
	}

	workspace := filepath.Clean(workspacePath)
	homeDir, _ := os.UserHomeDir()

	for _, token := range strings.Fields(command) {
		token = strings.Trim(token, `"'`)
		if token == "" {
			continue
		}
		var abs string
		switch {
		case filepath.IsAbs(token):
			abs = filepath.Clean(token)
		case token == "~":
			abs = homeDir
		case strings.HasPrefix(token, "~/"):
			abs = filepath.Clean(filepath.Join(homeDir, token[2:]))
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
