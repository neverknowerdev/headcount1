package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateCommandPaths(t *testing.T) {
	workspace := t.TempDir()
	homeDir, _ := os.UserHomeDir()

	cases := []struct {
		name    string
		cmd     string
		wantErr bool
	}{
		// Safe relative commands
		{"relative path", "ls src/", false},
		{"current dir", "go test ./...", false},
		{"no path", "echo hello", false},
		{"safe relative traversal within workspace", "ls ./subdir", false},
		// Absolute paths outside workspace
		{"absolute outside", "ls /etc/passwd", true},
		{"absolute tmp", "cat /tmp/secret", true},
		// Home-dir paths (the main regression cases)
		{"tilde home", "ls ~/Code/paperclip2", true},
		{"tilde home in compound", "echo foo && ls -la ~/Code/paperclip2 2>/dev/null", true},
		{"cd tilde", "cd ~/Code/paperclip2 && codegraph status", true},
		{"bare tilde", "cd ~", true},
		// $HOME variable references
		{"dollar HOME", "ls $HOME/secret", true},
		{"dollar HOME braces", "cd ${HOME}/Code", true},
		// Relative traversal outside workspace
		{"dot dot escape", "cat ../../etc/passwd", true},
		// Absolute path that IS the workspace root is OK (edge case)
		{"workspace itself", "ls " + workspace, false},
		// Absolute path within workspace is OK
		{"workspace subdir", "ls " + filepath.Join(workspace, "src"), false},
		// Home dir that happens to equal workspace (unlikely but handle)
		{"home is workspace", "ls " + homeDir, homeDir != workspace},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommandPaths(workspace, tc.cmd)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for command %q, got nil", tc.cmd)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for command %q: %v", tc.cmd, err)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	workspace := t.TempDir()
	homeDir, _ := os.UserHomeDir()

	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"dot", ".", false},
		{"subdir", "src/main.go", false},
		{"absolute workspace", workspace, false},
		{"absolute outside", "/etc/passwd", true},
		{"tilde home", "~/Documents", homeDir != workspace},
		{"bare tilde", "~", homeDir != workspace},
		{"dot dot escape", "../../etc", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolvePath(workspace, tc.path)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for path %q, got nil", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for path %q: %v", tc.path, err)
			}
		})
	}
}
