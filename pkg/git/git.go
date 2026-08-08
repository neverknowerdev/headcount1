package git

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type GitManager struct {
	repoPath   string
	sshKeyPath string // concrete path to the private key used for `ssh -i`
	httpToken  string
}

const headcount1CoAuthorTrailer = "Co-authored-by: headcount1.io <headcount1@headcount1.io>"

// commitMessageWithHeadcount1Attribution adds the standard Git co-author
// trailer to commits created by Headcount1. GitHub renders this trailer as a
// co-author when the email is associated with a GitHub account.
func commitMessageWithHeadcount1Attribution(message string) string {
	message = strings.TrimSpace(message)
	for _, line := range strings.Split(message, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), headcount1CoAuthorTrailer) {
			return message
		}
	}
	return message + "\n\n" + headcount1CoAuthorTrailer
}

// NewGitManager builds a git manager. sshKeyPath is the private-key FILE to
// authenticate with (per-user, resolved by the caller); an empty path disables
// SSH auth. Historically this took the ssh DIRECTORY — callers now pass the
// resolved key file (see filesystem.ResolveSSHKeyPath).
func NewGitManager(repoPath, sshKeyPath string) *GitManager {
	return &GitManager{
		repoPath:   repoPath,
		sshKeyPath: sshKeyPath,
	}
}

// WithHTTPToken makes Git authenticate to GitHub over HTTPS without putting a
// credential in the remote URL or command arguments.
func (g *GitManager) WithHTTPToken(token string) *GitManager { g.httpToken = token; return g }

func (g *GitManager) runGitCommand(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.repoPath
	cmd.Env = g.withGitEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git command failed: %s, output: %s", err, string(output))
	}
	return string(output), nil
}

// isLocalOnly reports whether the configured repo is on the local filesystem
// (file:// or a plain absolute path) and therefore does not need SSH.
func (g *GitManager) isLocalOnly() bool {
	p := g.repoPath
	return strings.HasPrefix(p, "file://") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
}

func usesSSH(repoURL string) bool {
	return strings.HasPrefix(repoURL, "git@") || strings.HasPrefix(repoURL, "ssh://")
}

func (g *GitManager) Init(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(g.repoPath, ".git")); os.IsNotExist(err) {
		_, err := g.runGitCommand(ctx, "init")
		return err
	}
	return nil
}

func (g *GitManager) SetRemote(ctx context.Context, remoteURL string) error {
	if remoteURL == "" {
		return nil
	}
	out, err := g.runGitCommand(ctx, "remote")
	if err != nil {
		return err
	}
	if strings.Contains(out, "origin") {
		_, err = g.runGitCommand(ctx, "remote", "set-url", "origin", remoteURL)
	} else {
		_, err = g.runGitCommand(ctx, "remote", "add", "origin", remoteURL)
	}
	return err
}

func (g *GitManager) CommitAndPush(ctx context.Context, message string) error {
	_, err := g.runGitCommand(ctx, "add", ".")
	if err != nil {
		return err
	}

	// Check if there are changes to commit
	statusOut, _ := g.runGitCommand(ctx, "status", "--porcelain")
	if strings.TrimSpace(statusOut) == "" {
		return nil // Nothing to commit
	}

	// Make sure user config exists
	g.runGitCommand(ctx, "config", "user.name", "Agent Orchestrator")
	g.runGitCommand(ctx, "config", "user.email", "agent@headcount1.local")

	_, err = g.runGitCommand(ctx, "commit", "-m", commitMessageWithHeadcount1Attribution(message))
	if err != nil {
		return err
	}

	// Push if remote exists
	out, _ := g.runGitCommand(ctx, "remote")
	if strings.Contains(out, "origin") {
		_, err = g.runGitCommand(ctx, "push", "origin", "main", "--force")
		// if main doesn't exist, try master
		if err != nil {
			g.runGitCommand(ctx, "push", "origin", "master", "--force")
		}
	}

	return nil
}

// sshCommandFor builds the GIT_SSH_COMMAND for a key path, with host-key
// verification enabled. The mode is HEADCOUNT1_GIT_STRICT_HOST_KEY_CHECKING
// (default "accept-new": trust a host's key on first contact, then detect any
// later change — MITM protection that "no" + /dev/null threw away entirely).
// Set "yes" to require a pre-seeded known_hosts, or "no" to restore the old
// no-verification behavior. The known_hosts lives next to the key so first-seen
// host keys persist across ops.
func sshCommandFor(keyPath string) string {
	mode := os.Getenv("HEADCOUNT1_GIT_STRICT_HOST_KEY_CHECKING")
	if mode == "" {
		mode = "accept-new"
	}
	knownHosts := filepath.Join(filepath.Dir(keyPath), "known_hosts")
	return fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=%s -o UserKnownHostsFile=%s -o BatchMode=yes",
		keyPath, mode, knownHosts)
}

// sshEnv returns the GIT_SSH_COMMAND env string for the configured key.
// Returns "" if the URL is a local file:// path (SSH is irrelevant).
func (g *GitManager) sshEnv() string {
	if g.isLocalOnly() || g.sshKeyPath == "" {
		return ""
	}
	return sshCommandFor(g.sshKeyPath)
}

// withGitEnv returns os.Environ() plus GIT_SSH_COMMAND when appropriate.
// GIT_TERMINAL_PROMPT=0 ensures git never prompts for credentials interactively.
func (g *GitManager) withGitEnv() []string {
	env := os.Environ()
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	if g.httpToken != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.httpToken))
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+basic)
	}
	// A GitHub App token is an HTTPS credential. Do not inject an unrelated
	// SSH command into those operations; manual SSH repositories never have an
	// HTTP token.
	if g.httpToken == "" {
		if sshCmd := g.sshEnv(); sshCmd != "" {
			env = append(env, "GIT_SSH_COMMAND="+sshCmd)
		}
	}
	return env
}

func (g *GitManager) CloneOrFetchProject(ctx context.Context, repoURL, targetDir string) error {
	// If the target dir doesn't exist, or exists but isn't a git repository
	// (e.g. an empty directory created by the filesystem manager), remove
	// any leftover and clone fresh.
	needsClone := false
	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		needsClone = true
	} else if _, err := os.Stat(filepath.Join(targetDir, ".git")); os.IsNotExist(err) {
		// Existing dir without a .git — wipe it and clone fresh.
		if err := os.RemoveAll(targetDir); err != nil {
			return fmt.Errorf("failed to clean stale target dir: %w", err)
		}
		needsClone = true
	}

	if needsClone {
		cmd := exec.CommandContext(ctx, "git", "clone", repoURL, targetDir)
		cmd.Env = g.withGitEnv()

		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git clone failed: %v, output: %s", err, string(out))
		}
		return nil
	}

	// Existing repo — just fetch
	cmd := exec.CommandContext(ctx, "git", "fetch", "--all")
	cmd.Dir = targetDir
	cmd.Env = g.withGitEnv()

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch failed: %v, output: %s", err, string(out))
	}
	return nil
}

func (g *GitManager) CreateWorktree(ctx context.Context, baseRepoDir, targetWorktreeDir, branchName string, baseBranch string) error {
	if baseBranch == "" {
		baseBranch = "origin/main"
	}
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", branchName, targetWorktreeDir, baseBranch)
	cmd.Dir = baseRepoDir
	cmd.Env = g.withGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		// A caller that selected a specific base branch must never silently get
		// a worktree from whichever branch happens to be checked out locally.
		// The main/master compatibility fallback below is only for legacy
		// repositories whose default branch is master.
		if baseBranch != "origin/main" {
			return fmt.Errorf("git worktree add from %s failed: %v, output: %s", baseBranch, err, string(out))
		}
		// try master if main failed
		if baseBranch == "origin/main" {
			cmd = exec.CommandContext(ctx, "git", "worktree", "add", "-b", branchName, targetWorktreeDir, "origin/master")
			cmd.Dir = baseRepoDir
			cmd.Env = g.withGitEnv()
			out, err = cmd.CombinedOutput()
		}
		if err != nil {
			// fallback to a detached branch
			cmd = exec.CommandContext(ctx, "git", "worktree", "add", "-b", branchName, targetWorktreeDir)
			cmd.Dir = baseRepoDir
			cmd.Env = g.withGitEnv()
			out, err = cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("git worktree add failed: %v, output: %s", err, string(out))
			}
		}
	}
	return nil
}

// ValidateBranchName uses Git's own branch-name rules. Keeping this check
// close to git execution lets API callers reject malformed base branches early
// while still supporting valid names such as release/2026.08.
func ValidateBranchName(ctx context.Context, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("branch name is required")
	}
	cmd := exec.CommandContext(ctx, "git", "check-ref-format", "--branch", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("invalid branch name %q: %s", branch, strings.TrimSpace(string(out)))
	}
	return nil
}

// ListRemoteBranches refreshes remote references, then returns branch names on
// origin ordered by the newest tip commit first. Git does not store a remote
// branch creation timestamp; its tip commit time is the stable, repository
// native proxy for "recently created" branches. It never changes the caller's
// checkout.
func (g *GitManager) ListRemoteBranches(ctx context.Context) ([]string, error) {
	fetch := exec.CommandContext(ctx, "git", "fetch", "--prune", "origin")
	fetch.Dir = g.repoPath
	fetch.Env = g.withGitEnv()
	if out, err := fetch.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("could not refresh remote branches: %v, output: %s", err, string(out))
	}
	cmd := exec.CommandContext(ctx, "git", "for-each-ref", "--sort=-committerdate", "--format=%(refname:strip=3)", "refs/remotes/origin")
	cmd.Dir = g.repoPath
	cmd.Env = g.withGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("could not list remote branches: %v, output: %s", err, string(out))
	}
	var branches []string
	for _, branch := range strings.Fields(string(out)) {
		if branch == "HEAD" || branch == "" {
			continue
		}
		branches = append(branches, branch)
	}
	return branches, nil
}

func (g *GitManager) ValidateRemote(ctx context.Context, repoURL string) error {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", repoURL)
	cmd.Env = g.validateRemoteEnv(repoURL)

	out, err := cmd.CombinedOutput()
	if err != nil {
		output := string(out)
		if strings.Contains(output, "Permission denied") ||
			strings.Contains(output, "Authentication failed") ||
			strings.Contains(output, "could not read Username") ||
			strings.Contains(output, "Could not read from remote repository") ||
			strings.Contains(output, "Host key verification failed") {
			return fmt.Errorf("[auth] %s", output)
		}
		return fmt.Errorf("git remote validation failed: %v, output: %s", err, output)
	}
	return nil
}

// validateRemoteEnv only overrides SSH when a user actually supplied a key.
// Without one, Git must retain its normal SSH-agent/host configuration rather
// than being forced to run `ssh -i ` with an empty identity path.
func (g *GitManager) validateRemoteEnv(repoURL string) []string {
	// Build env based on the URL, not the manager's repoPath, since this can be
	// called for arbitrary URLs during project validation.
	env := append([]string{}, os.Environ()...)
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	if usesSSH(repoURL) && strings.TrimSpace(g.sshKeyPath) != "" {
		env = append(env, "GIT_SSH_COMMAND="+sshCommandFor(g.sshKeyPath))
	}
	return env
}

func (g *GitManager) Pull(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "pull")
	cmd.Dir = g.repoPath
	cmd.Env = g.withGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull failed: %v, output: %s", err, string(out))
	}
	return nil
}

func (g *GitManager) RemoveWorktree(ctx context.Context, worktreeDir string) error {
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", worktreeDir, "--force")
	cmd.Dir = g.repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove failed: %v, output: %s", err, string(out))
	}
	return nil
}

func (g *GitManager) GetDiff(ctx context.Context) (string, error) {
	out, err := g.runGitCommand(ctx, "diff")
	if err != nil {
		return "", err
	}
	return out, nil
}

func (g *GitManager) GetDiffInDir(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %v, output: %s", err, string(out))
	}
	return string(out), nil
}

func (g *GitManager) CommitInWorktree(ctx context.Context, worktreeDir, message string) error {
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = worktreeDir
		cmd.Env = g.withGitEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %v failed: %v, output: %s", args, err, string(out))
		}
		return string(out), nil
	}

	if _, err := run("add", "."); err != nil {
		return err
	}

	statusOut, _ := run("status", "--porcelain")
	if strings.TrimSpace(statusOut) == "" {
		return nil
	}

	run("config", "user.name", "Agent Orchestrator")
	run("config", "user.email", "agent@headcount1.local")

	if _, err := run("commit", "-m", commitMessageWithHeadcount1Attribution(message)); err != nil {
		return err
	}
	return nil
}

// PushWorktreeBranch publishes an agent branch; callers create a PR rather
// than merging or force-pushing the default branch.
func (g *GitManager) PushWorktreeBranch(ctx context.Context, worktreeDir, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", branch)
	cmd.Dir = worktreeDir
	cmd.Env = g.withGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %v, output: %s", err, string(out))
	}
	return nil
}

func (g *GitManager) MergeBranch(ctx context.Context, baseRepoDir, branchName string) error {
	// Simple merge strategy (checkout main, pull, merge branch, push)
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = baseRepoDir
		cmd.Env = g.withGitEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %v failed: %v, output: %s", args, err, string(out))
		}
		return nil
	}

	if err := run("checkout", "main"); err != nil {
		if err := run("checkout", "master"); err != nil {
			return err
		}
	}
	run("pull")
	if err := run("merge", branchName); err != nil {
		return err
	}
	if err := run("push"); err != nil {
		return err
	}
	return nil
}
