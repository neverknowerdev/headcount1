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
	repoPath  string
	sshDir    string
	httpToken string
}

// WithHTTPToken makes Git authenticate to GitHub over HTTPS without putting a
// credential in the remote URL or command arguments.
func (g *GitManager) WithHTTPToken(token string) *GitManager { g.httpToken = token; return g }

func NewGitManager(repoPath, sshDir string) *GitManager {
	return &GitManager{
		repoPath: repoPath,
		sshDir:   sshDir,
	}
}

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

	_, err = g.runGitCommand(ctx, "commit", "-m", message)
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

// sshEnv returns the GIT_SSH_COMMAND env string for the configured key.
// Returns "" if the URL is a local file:// path (SSH is irrelevant).
func (g *GitManager) sshEnv() string {
	if g.isLocalOnly() {
		return ""
	}
	keyPath := filepath.Join(g.sshDir, "id_rsa")
	return fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes", keyPath)
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
	if g.httpToken != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.httpToken))
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+basic)
	}
	if sshCmd := g.sshEnv(); sshCmd != "" {
		env = append(env, "GIT_SSH_COMMAND="+sshCmd)
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

func (g *GitManager) ValidateRemote(ctx context.Context, repoURL string) error {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", repoURL)

	// Build env based on the URL, not the manager's repoPath, since this can be called
	// for arbitrary URLs during CreateProject validation.
	env := os.Environ()
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	if !strings.HasPrefix(repoURL, "file://") && !strings.HasPrefix(repoURL, "/") && !strings.HasPrefix(repoURL, "./") && !strings.HasPrefix(repoURL, "../") {
		keyPath := filepath.Join(g.sshDir, "id_rsa")
		env = append(env, fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes", keyPath))
	}
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err != nil {
		output := string(out)
		if strings.Contains(output, "Permission denied") ||
			strings.Contains(output, "Authentication failed") ||
			strings.Contains(output, "Could not read from remote repository") ||
			strings.Contains(output, "Host key verification failed") {
			return fmt.Errorf("[auth] %s", output)
		}
		return fmt.Errorf("git remote validation failed: %v, output: %s", err, output)
	}
	return nil
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

	if _, err := run("commit", "-m", message); err != nil {
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
