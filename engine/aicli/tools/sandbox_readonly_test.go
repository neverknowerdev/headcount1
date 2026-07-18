package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover the read-only-root threading: the artifacts directory
// ({basePath}/artifacts/{company}/{rootTaskID}) and a parent task's workdir
// are passed as readOnlyDirs — readable by the file tools and referencable
// in shell commands, but never writable.

func setupReadOnlyDirs(t *testing.T) (workspace, artifactsDir string) {
	t.Helper()
	base := t.TempDir()
	workspace = filepath.Join(base, "workspace", "acme", "task-7")
	artifactsDir = filepath.Join(base, "artifacts", "acme", "7")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactsDir, "report.md"), []byte("# report"), 0644); err != nil {
		t.Fatal(err)
	}
	return workspace, artifactsDir
}

func TestResolveReadPathAllowsReadOnlyRoots(t *testing.T) {
	workspace, artifactsDir := setupReadOnlyDirs(t)
	roDirs := []string{artifactsDir}

	// Absolute path under the read-only root resolves for reads.
	got, err := resolveReadPath(workspace, roDirs, filepath.Join(artifactsDir, "report.md"))
	if err != nil {
		t.Fatalf("read under read-only root rejected: %v", err)
	}
	if got != filepath.Join(artifactsDir, "report.md") {
		t.Fatalf("resolved to %q", got)
	}

	// ...but not for writes: resolvePath (the write path) must still reject it.
	if _, err := resolvePath(workspace, filepath.Join(artifactsDir, "report.md")); err == nil {
		t.Fatal("write path must reject the read-only root")
	}

	// Paths outside both the workspace and the read-only roots stay rejected.
	if _, err := resolveReadPath(workspace, roDirs, "/etc/passwd"); err == nil {
		t.Fatal("read outside workspace and read-only roots must be rejected")
	}

	// Sibling dir that shares the root's name as a prefix must not match.
	if _, err := resolveReadPath(workspace, roDirs, artifactsDir+"-evil/x"); err == nil {
		t.Fatal("prefix-sibling of a read-only root must be rejected")
	}
}

func TestReadFileToolWithReadOnlyDirs(t *testing.T) {
	workspace, artifactsDir := setupReadOnlyDirs(t)

	rf := NewReadFile(workspace, artifactsDir)
	args, _ := json.Marshal(map[string]string{"path": filepath.Join(artifactsDir, "report.md")})
	out, err := rf.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("ReadFile under read-only root failed: %v", err)
	}
	if !strings.Contains(out, "# report") {
		t.Fatalf("unexpected content: %q", out)
	}

	// WriteFile must not accept the read-only root.
	wf := NewWriteFile(workspace)
	wargs, _ := json.Marshal(map[string]string{"path": filepath.Join(artifactsDir, "evil.md"), "content": "x"})
	if _, err := wf.Execute(context.Background(), wargs); err == nil {
		t.Fatal("WriteFile into the read-only artifacts dir must be rejected")
	}
}

func TestValidateCommandPathsWithReadOnlyDirs(t *testing.T) {
	workspace, artifactsDir := setupReadOnlyDirs(t)
	roDirs := []string{artifactsDir}

	// Referencing a file under the read-only root is allowed (reads are fine;
	// the kernel sandbox blocks writes).
	if err := validateCommandPaths(workspace, roDirs, "cat "+filepath.Join(artifactsDir, "report.md")); err != nil {
		t.Fatalf("command referencing read-only root rejected: %v", err)
	}

	// Other absolute paths stay rejected.
	if err := validateCommandPaths(workspace, roDirs, "cat /etc/passwd"); err == nil {
		t.Fatal("command referencing /etc/passwd must be rejected")
	}
}
