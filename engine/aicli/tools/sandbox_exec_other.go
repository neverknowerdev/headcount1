//go:build !linux && !darwin

package tools

import (
	"context"
	"os/exec"
)

func sandboxDescription() string {
	return "path validation only — no kernel-level sandbox on this platform"
}

// sandboxedCommand on platforms without a kernel sandbox backend (notably
// Windows) runs the command directly; validateCommandPaths is the only guard.
func sandboxedCommand(ctx context.Context, workspacePath, command string) (*exec.Cmd, func(), error) {
	return exec.CommandContext(ctx, "sh", "-c", command), nil, nil
}
