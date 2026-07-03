//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func execBash(t *testing.T, workspace, command string) string {
	t.Helper()
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	out, err := NewExecCommand(workspace).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute(%q) returned error: %v", command, err)
	}
	return out
}

// outsideDir returns a directory that is outside the workspace AND outside
// every extra writable dir (temp, caches), so only the workspace rule could
// permit writes to it. Creates it under the home directory.
func outsideDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	dir, err := os.MkdirTemp(home, "sandbox-escape-test-*")
	if err != nil {
		t.Skipf("cannot create test dir in home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestLandlockBlocksWriteOutsideWorkspace(t *testing.T) {
	if landlockABI() == 0 {
		t.Skip("kernel lacks Landlock support")
	}
	workspace := t.TempDir()
	outside := outsideDir(t)

	// Assign the outside path to a shell variable so validateCommandPaths
	// cannot catch it — only the kernel sandbox can stop this write.
	out := execBash(t, workspace, fmt.Sprintf(`D=%s; echo pwned > "$D/pwned.txt" && echo WROTE || echo BLOCKED`, outside))
	if !strings.Contains(out, "BLOCKED") {
		t.Errorf("expected write to be blocked, got output: %q", out)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Errorf("file was created outside the workspace (stat err: %v)", err)
	}
}

func TestLandlockBlocksDeleteOutsideWorkspace(t *testing.T) {
	if landlockABI() == 0 {
		t.Skip("kernel lacks Landlock support")
	}
	workspace := t.TempDir()
	outside := outsideDir(t)
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := execBash(t, workspace, fmt.Sprintf(`D=%s; rm "$D/victim.txt" && echo DELETED || echo BLOCKED`, outside))
	if !strings.Contains(out, "BLOCKED") {
		t.Errorf("expected delete to be blocked, got output: %q", out)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("file outside the workspace was deleted: %v", err)
	}
}

func TestLandlockBlocksSymlinkEscapeWrite(t *testing.T) {
	if landlockABI() == 0 {
		t.Skip("kernel lacks Landlock support")
	}
	workspace := t.TempDir()
	outside := outsideDir(t)
	if err := os.Symlink(outside, filepath.Join(workspace, "innocent")); err != nil {
		t.Fatal(err)
	}

	// The symlink lives inside the workspace, but its target does not.
	// Landlock resolves the real path, so the write must fail. Note this is
	// also caught by validateCommandPaths via os.Root; go through a variable
	// to test the kernel layer in isolation.
	out := execBash(t, workspace, `L=innocent; echo pwned > "$L/pwned.txt" && echo WROTE || echo BLOCKED`)
	if !strings.Contains(out, "BLOCKED") {
		t.Errorf("expected symlink-escape write to be blocked, got output: %q", out)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Errorf("file was created outside the workspace via symlink (stat err: %v)", err)
	}
}

func TestLandlockAllowsWorkInsideWorkspace(t *testing.T) {
	if landlockABI() == 0 {
		t.Skip("kernel lacks Landlock support")
	}
	workspace := t.TempDir()

	out := execBash(t, workspace, `echo hello > file.txt && mkdir -p a b && echo x > a/f && mv a/f b/f && cat file.txt`)
	if !strings.Contains(out, "hello") {
		t.Errorf("expected in-workspace commands to succeed, got output: %q", out)
	}
	if _, err := os.Stat(filepath.Join(workspace, "b", "f")); err != nil {
		t.Errorf("mv across directories inside the workspace failed: %v", err)
	}
}

func TestLandlockAllowsReadsOutsideWorkspace(t *testing.T) {
	if landlockABI() == 0 {
		t.Skip("kernel lacks Landlock support")
	}
	workspace := t.TempDir()

	// Reads are intentionally unrestricted (toolchains live outside the
	// workspace). Go through a variable: validateCommandPaths rejects
	// explicit absolute paths, but the kernel layer allows the read.
	out := execBash(t, workspace, `F=/etc/hostname; cat "$F" >/dev/null 2>&1; ls / >/dev/null && echo READ_OK`)
	if !strings.Contains(out, "READ_OK") {
		t.Errorf("expected reads outside the workspace to work, got output: %q", out)
	}
}

func TestLandlockInheritedByChildProcesses(t *testing.T) {
	if landlockABI() == 0 {
		t.Skip("kernel lacks Landlock support")
	}
	workspace := t.TempDir()
	outside := outsideDir(t)

	// The restriction must survive into subshells and child processes.
	out := execBash(t, workspace, fmt.Sprintf(`D=%s; sh -c "echo pwned > $D/pwned.txt" && echo WROTE || echo BLOCKED`, outside))
	if !strings.Contains(out, "BLOCKED") {
		t.Errorf("expected child-process write to be blocked, got output: %q", out)
	}
}

// TestSandboxReexecChild exercises the re-exec plumbing itself (marker arg →
// MaybeRunSandboxChild → landlock BestEffort → exec sh) regardless of kernel
// Landlock support, since BestEffort degrades to a no-op on old kernels.
func TestSandboxReexecChild(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	out, err := exec.Command(self, sandboxChildArg, workspace, "echo child-ok").CombinedOutput()
	if err != nil {
		t.Fatalf("re-exec child failed: %v (output %q)", err, out)
	}
	if !strings.Contains(string(out), "child-ok") {
		t.Errorf("unexpected child output: %q", out)
	}
}

func TestSandboxedCommandFallsBackWithoutLandlock(t *testing.T) {
	// Sanity-check the plumbing: sandboxedCommand never fails hard on a
	// supported kernel, and produces a runnable command.
	cmd, cleanup, err := sandboxedCommand(context.Background(), t.TempDir(), "echo ok")
	if err != nil {
		t.Fatalf("sandboxedCommand: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running sandboxed command: %v (output %q)", err, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Errorf("unexpected output: %q", out)
	}
}
