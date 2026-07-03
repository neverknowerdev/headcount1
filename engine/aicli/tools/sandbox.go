package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
//
// This is a UX layer, not the security boundary: it gives the model a clear,
// actionable error for obvious escapes before the command runs. The actual
// enforcement is the kernel sandbox (see sandbox_exec.go). Paths that exist
// are additionally checked through os.Root, which catches symlinks inside the
// workspace that point outside it.
func validateCommandPaths(workspacePath, command string) error {
	// Reject $HOME / ${HOME} variable references that could bypass path checking.
	if strings.Contains(command, "$HOME") || strings.Contains(command, "${HOME}") {
		return fmt.Errorf("command references $HOME which is outside the workspace; use paths relative to the workspace root")
	}

	workspace := filepath.Clean(workspacePath)
	homeDir, _ := os.UserHomeDir()

	root, err := os.OpenRoot(workspace)
	if err != nil {
		return fmt.Errorf("cannot open workspace %q: %w", workspace, err)
	}
	defer root.Close()

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
			return fmt.Errorf("path %q escapes the workspace (%s); use paths relative to the workspace root", token, workspace)
		}
		rel, err := filepath.Rel(workspace, abs)
		if err != nil {
			continue
		}
		// os.Root resolves the path with the workspace as an inescapable
		// root, so a symlink pointing outside fails here even though the
		// textual check above passed.
		// Missing paths (e.g. output files) and not-a-directory components
		// are fine here — the shell will report those itself.
		if _, err := root.Stat(rel); err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return fmt.Errorf("path %q escapes the workspace (%s) when resolved: %v", token, workspace, err)
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
