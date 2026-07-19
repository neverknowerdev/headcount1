package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// sandboxHardening holds the optional, config-gated extra restrictions layered
// on top of the always-on write sandbox. Everything here is OFF by default, so
// an unconfigured single-uid dev/CI box behaves exactly as before — these only
// engage when an operator opts in via env, on a deployment set up for them.
type sandboxHardening struct {
	// uid/gid > 0 → run the agent's shell as this dedicated, unprivileged user
	// instead of the server's uid. The server's secret files (SQLite DB,
	// keystore, the graceful-exit keyring snapshot, SSH keys) are 0600-owned by
	// the server uid, so a different sandbox uid cannot read them — and cannot
	// read the server's /proc/<pid>/environ either (the kernel restricts that
	// to the owning uid). Requires the server to hold CAP_SETUID.
	uid, gid int
	// readScoping → replace Landlock's "read everything" rule with an explicit
	// allowlist of system roots that OMITS the server's home directory, so the
	// agent cannot read ~/.headcount1 secrets even when it shares the server's
	// uid. Defense-in-depth alongside (or instead of) a dedicated uid.
	readScoping bool
}

// active reports whether any hardening is configured (so the sandbox re-exec
// needs to carry the extra child config).
func (h sandboxHardening) active() bool { return h.uid > 0 || h.readScoping }

func loadSandboxHardening() sandboxHardening {
	h := sandboxHardening{uid: envInt("HEADCOUNT1_SANDBOX_UID"), gid: envInt("HEADCOUNT1_SANDBOX_GID")}
	if h.gid == 0 {
		h.gid = h.uid // default the group to the dedicated uid's value
	}
	switch strings.ToLower(os.Getenv("HEADCOUNT1_SANDBOX_READ_SCOPING")) {
	case "1", "true", "yes", "on":
		h.readScoping = true
	}
	return h
}

func envInt(key string) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// scrubbedEnv returns the process environment with the server's secrets
// removed, for handing to the agent's shell. The agent keeps a normal working
// environment (PATH, tool caches, project vars) but never sees the master/boot
// key, Vault/cloud credentials, the database URL, or SMTP secrets — so `env`
// and /proc/self/environ can't leak them.
func scrubbedEnv() []string {
	all := os.Environ()
	out := make([]string, 0, len(all))
	for _, kv := range all {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		if isServerSecretEnv(kv[:eq]) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func isServerSecretEnv(key string) bool {
	up := strings.ToUpper(key)
	// The app's own crown jewels + cloud creds + mailer + ssh-agent, by prefix.
	// SMTP_ covers SMTP_USERNAME/SMTP_HOST/SMTP_FROM (SMTP_PASSWORD is also
	// caught by the PASSWORD substring); SSH_ covers SSH_AUTH_SOCK /
	// SSH_AGENT_PID, which would otherwise let a same-uid child sign with the
	// server's forwarded ssh-agent.
	for _, p := range []string{"HEADCOUNT1_", "VAULT_", "AWS_", "AZURE_", "GCP_", "GOOGLE_", "SMTP_", "SSH_"} {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	switch up {
	case "DATABASE_URL", "REDIS_URL":
		return true
	}
	// Catch third-party secrets the operator may have in the server env
	// (GITHUB_TOKEN, NPM_TOKEN, OPENAI_API_KEY, STRIPE_SECRET_KEY,
	// DOCKER_PASSWORD, SIGNING_KEY, DEPLOY_KEY, SENTRY_DSN, …) by well-known
	// secret-ish substrings, so the model-driven shell can't read them via
	// `env` / /proc/self/environ. This is a best-effort denylist: an
	// unconventionally named secret can still slip through, so the strongest
	// isolation remains the dedicated sandbox uid (see doc/sandbox-hardening.md).
	for _, sub := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "PASSPHRASE", "CREDENTIAL", "APIKEY", "API_KEY", "PRIVATE_KEY", "ACCESS_KEY", "KEY", "DSN", "URI"} {
		if strings.Contains(up, sub) {
			return true
		}
	}
	return false
}

// sandboxEnvForHome returns the scrubbed environment with HOME/USER/LOGNAME
// pointed at the dedicated sandbox uid's home. Without this the child inherits
// the server uid's HOME, so tools that write $HOME/.cache would target a
// directory the sandbox uid can't write (and that read-scoping blocks), while
// the writable cache dir granted for that uid goes unused. Only used on the
// dedicated-uid path, where the override is correct.
func sandboxEnvForHome(home, username string) []string {
	env := scrubbedEnv()
	if home != "" {
		env = overrideEnvVar(env, "HOME", home)
	}
	if username != "" {
		env = overrideEnvVar(env, "USER", username)
		env = overrideEnvVar(env, "LOGNAME", username)
	}
	return env
}

// overrideEnvVar sets key=val in a KEY=VALUE slice, replacing an existing entry
// or appending a new one.
func overrideEnvVar(env []string, key, val string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}

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

// resolveReadPath resolves path like resolvePath, but additionally accepts
// absolute paths under any of the extra read-only roots (e.g. the parent
// task's workdir or the artifacts directory). Writes must keep using
// resolvePath — read-only roots are never writable.
func resolveReadPath(workspacePath string, readOnlyDirs []string, path string) (string, error) {
	resolved, err := resolvePath(workspacePath, path)
	if err == nil {
		return resolved, nil
	}
	if filepath.IsAbs(path) {
		abs := filepath.Clean(path)
		for _, dir := range readOnlyDirs {
			root := filepath.Clean(dir)
			if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
				return abs, nil
			}
		}
	}
	if len(readOnlyDirs) > 0 {
		return "", fmt.Errorf("path %q escapes the workspace and the read-only dirs (%s)", path, strings.Join(readOnlyDirs, ", "))
	}
	return "", err
}

// validateCommandPaths rejects shell commands that reference paths outside the
// workspace — absolute paths, home-dir (~) paths, and relative traversals alike.
//
// This is a UX layer, not the security boundary: it gives the model a clear,
// actionable error for obvious escapes before the command runs. The actual
// enforcement is the kernel sandbox (see sandbox_exec.go). Paths that exist
// are additionally checked through os.Root, which catches symlinks inside the
// workspace that point outside it.
func validateCommandPaths(workspacePath string, readOnlyDirs []string, command string) error {
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
			// Absolute references into a read-only root are fine: the kernel
			// sandbox only restricts writes, and these roots are meant to be
			// readable (parent task workdir, artifacts dir).
			allowed := false
			for _, dir := range readOnlyDirs {
				root := filepath.Clean(dir)
				if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
					allowed = true
					break
				}
			}
			if allowed {
				continue
			}
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
