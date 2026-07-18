//go:build darwin

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func sandboxDescription() string {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return "DISABLED — sandbox-exec not found; only path validation applies"
	}
	return "sandbox-exec (Seatbelt) — writes restricted to the workspace"
}

// sandboxedCommand runs `sh -c command` under macOS Seatbelt via sandbox-exec
// with a generated profile that denies file writes outside the workspace,
// temp dirs, and toolchain caches. The profile is written to a temp file; the
// returned cleanup func removes it.
func sandboxedCommand(ctx context.Context, workspacePath, command string, readOnlyDirs []string) (*exec.Cmd, func(), error) {
	sbExec, err := exec.LookPath("sandbox-exec")
	if err != nil {
		// No sandbox-exec available: run unsandboxed, same as the historical behavior.
		return exec.CommandContext(ctx, "sh", "-c", command), nil, nil
	}
	profile, err := seatbeltProfile(workspacePath)
	if err != nil {
		return nil, nil, fmt.Errorf("building sandbox profile: %w", err)
	}
	f, err := os.CreateTemp("", "headcount1-sandbox-*.sb")
	if err != nil {
		return nil, nil, fmt.Errorf("writing sandbox profile: %w", err)
	}
	if _, err := f.WriteString(profile); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("writing sandbox profile: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("writing sandbox profile: %w", err)
	}
	cleanup := func() { os.Remove(f.Name()) }
	cmd := exec.CommandContext(ctx, sbExec, "-f", f.Name(), "sh", "-c", command)
	return cmd, cleanup, nil
}

// seatbeltProfile generates a Seatbelt policy: allow everything, deny file
// writes, then re-allow writes under the workspace and the shared writable
// dirs. Later rules win, so the allow-list overrides the deny.
func seatbeltProfile(workspacePath string) (string, error) {
	writable := append([]string{workspacePath}, extraWritableDirs()...)

	var subpaths []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		subpaths = append(subpaths, p)
	}
	for _, dir := range writable {
		// Seatbelt matches canonical paths, and macOS temp dirs live behind
		// symlinks (/tmp -> /private/tmp, $TMPDIR under /private/var/folders),
		// so include both the literal and the resolved form.
		add(dir)
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			add(resolved)
		}
	}

	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	b.WriteString("(deny file-write*)\n")
	b.WriteString("(allow file-write*\n")
	for _, p := range subpaths {
		esc, err := escapeSeatbeltString(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "  (subpath \"%s\")\n", esc)
	}
	// Device files that shell commands routinely write to.
	b.WriteString("  (literal \"/dev/null\")\n")
	b.WriteString("  (literal \"/dev/zero\")\n")
	b.WriteString("  (literal \"/dev/tty\")\n")
	b.WriteString("  (literal \"/dev/dtracehelper\")\n")
	b.WriteString("  (regex #\"^/dev/ttys[0-9]+$\")\n")
	b.WriteString(")\n")
	return b.String(), nil
}

// escapeSeatbeltString escapes a path for embedding in a double-quoted
// Seatbelt (TinyScheme) string literal.
func escapeSeatbeltString(p string) (string, error) {
	if strings.ContainsAny(p, "\n\r") {
		return "", fmt.Errorf("path %q contains characters not representable in a sandbox profile", p)
	}
	p = strings.ReplaceAll(p, `\`, `\\`)
	p = strings.ReplaceAll(p, `"`, `\"`)
	return p, nil
}
