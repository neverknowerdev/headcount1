//go:build linux

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/landlock-lsm/go-landlock/landlock"
	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// sandboxChildArg is the hidden argv[1] marker that tells a re-executed copy
// of this binary to apply the Landlock ruleset and exec the shell command.
const sandboxChildArg = "__headcount-sandbox-child__"

// landlockABI returns the kernel's Landlock ABI version (0 if unsupported).
func landlockABI() int {
	v, err := llsys.LandlockGetABIVersion()
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func sandboxDescription() string {
	if v := landlockABI(); v > 0 {
		return fmt.Sprintf("Landlock (kernel ABI v%d) — writes restricted to the workspace", v)
	}
	return "DISABLED — kernel lacks Landlock support; only path validation applies"
}

// sandboxedCommand builds the command that runs `sh -c command` under a
// Landlock ruleset. Landlock rules apply to the calling process and are
// inherited by children, and Go cannot run code between fork and exec — so we
// re-execute our own binary, which applies the ruleset to itself (see
// MaybeRunSandboxChild) and then execs the shell in place.
func sandboxedCommand(ctx context.Context, workspacePath, command string) (*exec.Cmd, func(), error) {
	if landlockABI() == 0 {
		// No kernel support: run unsandboxed, same as the historical behavior.
		return exec.CommandContext(ctx, "sh", "-c", command), nil, nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot locate own executable for sandbox re-exec: %w", err)
	}
	return exec.CommandContext(ctx, self, sandboxChildArg, workspacePath, command), nil, nil
}

// MaybeRunSandboxChild must be called first thing in main() (and in TestMain
// of any test binary that executes the bash tool). When the process was
// spawned as a sandbox re-exec child it never returns: it applies the
// Landlock ruleset and replaces itself with `sh -c <command>` via execve, so
// the shell keeps this process's PID and the parent's timeout/kill still
// applies. In a normal process start it is a no-op.
func MaybeRunSandboxChild() {
	if len(os.Args) != 4 || os.Args[1] != sandboxChildArg {
		return
	}
	workspace, command := os.Args[2], os.Args[3]
	if err := restrictWritesToWorkspace(workspace); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: applying landlock rules: %v\n", err)
		os.Exit(125)
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox: sh not found in PATH")
		os.Exit(127)
	}
	if err := syscall.Exec(shell, []string{"sh", "-c", command}, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: exec sh: %v\n", err)
		os.Exit(126)
	}
}

// restrictWritesToWorkspace applies a Landlock ruleset to the current process:
// read access everywhere, write access only inside the workspace, temp dirs,
// and toolchain caches. BestEffort degrades gracefully on kernels with older
// Landlock ABIs (the parent already verified ABI >= 1 before re-executing).
func restrictWritesToWorkspace(workspace string) error {
	rw := append([]string{workspace}, extraWritableDirs()...)
	return landlock.V5.BestEffort().RestrictPaths(
		landlock.RODirs("/"),
		// WithRefer allows mv/ln across directories inside the writable trees
		// (a separate Landlock access right since ABI v2).
		landlock.RWDirs(rw...).WithRefer().IgnoreIfMissing(),
		landlock.RWFiles(
			"/dev/null", "/dev/zero", "/dev/full",
			"/dev/tty", "/dev/ptmx",
			"/dev/random", "/dev/urandom",
		).WithIoctlDev().IgnoreIfMissing(),
	)
}
