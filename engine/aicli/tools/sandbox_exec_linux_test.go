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

// TestReadRootsExcluding verifies the "/" minus secrets allowlist computation
// directly — it is pure filesystem enumeration, so it runs even on kernels
// without Landlock (where the integration test above skips).
func TestReadRootsExcluding(t *testing.T) {
	base := t.TempDir()
	// {base}/db + {base}/keyring.sealed are secret; {base}/repos is not.
	secretDir := filepath.Join(base, "db")
	sealed := filepath.Join(base, "keyring.sealed")
	sibling := filepath.Join(base, "repos")
	deepOK := filepath.Join(base, "workspace", "task-1")
	for _, d := range []string{secretDir, sibling, deepOK} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(sealed, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	roots := readRootsExcluding([]string{secretDir, sealed})
	set := map[string]bool{}
	for _, r := range roots {
		set[r] = true
	}

	// The excluded paths must NOT be granted.
	if set[secretDir] {
		t.Errorf("secret dir %q must not be in the read roots", secretDir)
	}
	if set[sealed] {
		t.Errorf("secret file %q must not be in the read roots", sealed)
	}
	// The base itself must not be granted wholesale (it contains a secret) — it
	// must be descended into instead.
	if set[base] {
		t.Errorf("base %q must be descended into, not granted whole", base)
	}
	// Non-secret siblings under the same base ARE granted.
	if !set[sibling] {
		t.Errorf("non-secret sibling %q must be readable, roots=%v", sibling, roots)
	}
	// A top-level system dir is granted (proves we reach the filesystem root).
	if !set["/usr"] && !set["/bin"] {
		t.Errorf("expected a top-level system root (/usr or /bin) to be granted, roots=%v", roots)
	}
	// Empty / root-only excludes are refused (never hide all of "/").
	if r := readRootsExcluding(nil); r != nil {
		t.Errorf("nil excludes must yield nil (fall back to RODirs(\"/\")), got %v", r)
	}
	if r := readRootsExcluding([]string{"/"}); r != nil {
		t.Errorf(`excluding "/" must be refused, got %v`, r)
	}
}

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

// TestLandlockProtectedDirsAlwaysDenied proves that registered secret paths are
// denied to the agent shell in the DEFAULT (broad-read) mode — no opt-in
// hardening — while everything else, including sibling dirs under the same
// parent, stays readable.
func TestLandlockProtectedDirsAlwaysDenied(t *testing.T) {
	if landlockABI() == 0 {
		t.Skip("kernel lacks Landlock support")
	}
	workspace := t.TempDir()

	// A "secrets" dir with a sibling the agent legitimately reads — like
	// {base}/db (secret) next to {base}/repos (fine). Both live under one
	// parent, so this also proves we carve out just the secret subtree, not the
	// whole parent.
	base := outsideDir(t)
	secretDir := filepath.Join(base, "db")
	siblingDir := filepath.Join(base, "repos")
	for _, d := range []string{secretDir, siblingDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	secretFile := filepath.Join(secretDir, "headcount1.db")
	if err := os.WriteFile(secretFile, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	siblingFile := filepath.Join(siblingDir, "readme.txt")
	if err := os.WriteFile(siblingFile, []byte("HELLO"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Baseline: with nothing registered, reads are open — the secret is readable.
	out := execBash(t, workspace, fmt.Sprintf(`F=%s; cat "$F" 2>/dev/null || echo BLOCKED`, secretFile))
	if !strings.Contains(out, "TOPSECRET") {
		t.Fatalf("baseline (no protected dirs) should read the file, got: %q", out)
	}

	// Register the secret dir as protected — no env hardening enabled.
	SetProtectedReadDirs([]string{secretDir})
	t.Cleanup(func() { SetProtectedReadDirs(nil) })

	// The secret is now denied.
	out = execBash(t, workspace, fmt.Sprintf(`F=%s; cat "$F" 2>/dev/null && echo LEAKED || echo BLOCKED`, secretFile))
	if strings.Contains(out, "TOPSECRET") || strings.Contains(out, "LEAKED") || !strings.Contains(out, "BLOCKED") {
		t.Errorf("protected secret dir must be denied, got: %q", out)
	}

	// Its sibling under the same parent is still readable (we hid only the
	// secret subtree, not the parent).
	out = execBash(t, workspace, fmt.Sprintf(`F=%s; cat "$F" 2>/dev/null || echo BLOCKED`, siblingFile))
	if !strings.Contains(out, "HELLO") {
		t.Errorf("a non-secret sibling dir must stay readable, got: %q", out)
	}

	// Sanity: workspace and system toolchain reads still work.
	out = execBash(t, workspace, `echo hi > f.txt && cat f.txt && cat /etc/hostname >/dev/null 2>&1 && echo OK`)
	if !strings.Contains(out, "hi") || !strings.Contains(out, "OK") {
		t.Errorf("protected-dirs mode broke normal workspace/system access, got: %q", out)
	}
}

// TestLandlockReadScopingHidesHome proves the opt-in read-scoping mode denies
// the agent read access to files under the server's home (where ~/.headcount1
// secrets live), while normal workspace and system-toolchain access still work.
func TestLandlockReadScopingHidesHome(t *testing.T) {
	if landlockABI() == 0 {
		t.Skip("kernel lacks Landlock support")
	}
	workspace := t.TempDir()
	// A planted "secret" directly under the home dir — like the DB, keystore, or
	// keyring snapshot under ~/.headcount1 — outside every granted root.
	secretDir := outsideDir(t)
	secretFile := filepath.Join(secretDir, "master.key")
	if err := os.WriteFile(secretFile, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Baseline: reads are open by default, so the secret is readable.
	out := execBash(t, workspace, fmt.Sprintf(`F=%s; cat "$F" 2>/dev/null || echo BLOCKED`, secretFile))
	if !strings.Contains(out, "TOPSECRET") {
		t.Fatalf("baseline (no read-scoping) should read the file, got: %q", out)
	}

	// With read-scoping the home is not in the allowlist → the read is denied.
	t.Setenv("HEADCOUNT1_SANDBOX_READ_SCOPING", "1")
	out = execBash(t, workspace, fmt.Sprintf(`F=%s; cat "$F" 2>/dev/null && echo LEAKED || echo BLOCKED`, secretFile))
	if strings.Contains(out, "TOPSECRET") || strings.Contains(out, "LEAKED") || !strings.Contains(out, "BLOCKED") {
		t.Errorf("read-scoping must hide the home secret, got: %q", out)
	}

	// Sanity: the agent can still work in its workspace and read the toolchain.
	out = execBash(t, workspace, `echo hi > f.txt && cat f.txt && cat /etc/hostname >/dev/null 2>&1 && echo OK`)
	if !strings.Contains(out, "hi") || !strings.Contains(out, "OK") {
		t.Errorf("read-scoping broke normal workspace/system access, got: %q", out)
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
	cmd, cleanup, err := sandboxedCommand(context.Background(), t.TempDir(), "echo ok", nil)
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
