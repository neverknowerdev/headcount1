package tools

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// The exec sandbox restricts shell commands so they can only WRITE inside the
// workspace (plus temp dirs and well-known toolchain caches). Reads are not
// restricted. Enforcement is kernel-level and per-platform:
//
//   - Linux:  Landlock (see sandbox_exec_linux.go). The current binary
//     re-executes itself, applies a Landlock ruleset to the child process,
//     then execs `sh -c <command>`. The restriction is inherited by every
//     descendant process.
//   - macOS:  sandbox-exec / Seatbelt (see sandbox_exec_darwin.go) with a
//     generated profile that denies file-write* outside the allowed paths.
//   - other (incl. Windows): no kernel enforcement; validateCommandPaths is
//     the only guard.
//
// validateCommandPaths (sandbox.go) still runs first on every platform — it
// gives the model a friendly, actionable error for obvious escapes instead of
// a mid-command "permission denied".
//
// Each platform file provides:
//
//	sandboxedCommand(ctx, workspacePath, command) (*exec.Cmd, func(), error)
//	sandboxDescription() string

var sandboxLogOnce sync.Once

// logSandboxMode logs the active exec-sandbox backend once per process.
func logSandboxMode() {
	sandboxLogOnce.Do(func() {
		log.Printf("exec sandbox: %s", sandboxDescription())
	})
}

// extraWritableDirs returns the writable dirs computed against the server's own
// home directory (the default, un-hardened path).
func extraWritableDirs() []string {
	home, _ := os.UserHomeDir()
	return extraWritableDirsForHome(home)
}

// extraWritableDirsForHome returns paths outside the workspace that sandboxed
// commands are still allowed to write to: temp dirs (many tools break without a
// writable TMPDIR) and well-known toolchain caches under `home`, so `go build`,
// npm & co keep working under the write restriction. When a dedicated sandbox
// uid is used the caches must resolve against THAT uid's home (not the server's,
// which the sandbox uid cannot write) — callers pass the target home here.
func extraWritableDirsForHome(home string) []string {
	dirs := []string{os.TempDir(), "/tmp", "/var/tmp", "/dev/shm"}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".cache"),    // GOCACHE, pip, uv, ...
			filepath.Join(home, ".npm"),      // npm cache
			filepath.Join(home, "go", "pkg"), // GOMODCACHE
		)
		if runtime.GOOS == "darwin" {
			dirs = append(dirs, filepath.Join(home, "Library", "Caches"))
		}
	}
	seen := make(map[string]bool, len(dirs))
	out := dirs[:0]
	for _, d := range dirs {
		d = filepath.Clean(d)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}
