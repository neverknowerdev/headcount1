package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type GitManager struct {
	repoPath string
	sshDir   string
}

func NewGitManager(repoPath, sshDir string) *GitManager {
	return &GitManager{
		repoPath: repoPath,
		sshDir:   sshDir,
	}
}

func (g *GitManager) runGitCommand(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.repoPath

	// Configure SSH to use the specific key and ignore host key checking
	keyPath := filepath.Join(g.sshDir, "id_rsa")
	sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null", keyPath)
	cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_SSH_COMMAND=%s", sshCmd))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git command failed: %s, output: %s", err, string(output))
	}
	return string(output), nil
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
	g.runGitCommand(ctx, "config", "user.email", "agent@paperclip.local")

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

func (g *GitManager) CloneOrFetchProject(ctx context.Context, repoURL, targetDir string) error {
	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		cmd := exec.CommandContext(ctx, "git", "clone", repoURL, targetDir)

		keyPath := filepath.Join(g.sshDir, "id_rsa")
		sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null", keyPath)
		cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_SSH_COMMAND=%s", sshCmd))

		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git clone failed: %v, output: %s", err, string(out))
		}
	} else {
		// Just fetch
		cmd := exec.CommandContext(ctx, "git", "fetch", "--all")
		cmd.Dir = targetDir

		keyPath := filepath.Join(g.sshDir, "id_rsa")
		sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null", keyPath)
		cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_SSH_COMMAND=%s", sshCmd))

		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git fetch failed: %v, output: %s", err, string(out))
		}
	}
	return nil
}

func (g *GitManager) CreateWorktree(ctx context.Context, baseRepoDir, targetWorktreeDir, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", branchName, targetWorktreeDir, "origin/main")
	cmd.Dir = baseRepoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		// try master
		cmd = exec.CommandContext(ctx, "git", "worktree", "add", "-b", branchName, targetWorktreeDir, "origin/master")
		cmd.Dir = baseRepoDir
		out, err = cmd.CombinedOutput()
		if err != nil {
			// just fallback to a detached branch
			cmd = exec.CommandContext(ctx, "git", "worktree", "add", "-b", branchName, targetWorktreeDir)
			cmd.Dir = baseRepoDir
			out, err = cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("git worktree add failed: %v, output: %s", err, string(out))
			}
		}
	}
	return nil
}

func (g *GitManager) MergeBranch(ctx context.Context, baseRepoDir, branchName string) error {
	// Simple merge strategy (checkout main, pull, merge branch, push)
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = baseRepoDir
		keyPath := filepath.Join(g.sshDir, "id_rsa")
		sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null", keyPath)
		cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_SSH_COMMAND=%s", sshCmd))
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
