package git

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithHTTPTokenAddsOneGitHubAuthorizationHeader(t *testing.T) {
	manager := NewGitManager("/tmp/repo", "").WithHTTPToken("installation-token")
	env := manager.withGitEnv()

	var count, keyCount, valueCount int
	for _, item := range env {
		switch {
		case item == "GIT_CONFIG_COUNT=1":
			count++
		case item == "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader":
			keyCount++
		case strings.HasPrefix(item, "GIT_CONFIG_VALUE_0="):
			valueCount++
			got := strings.TrimPrefix(item, "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic ")
			decoded, err := base64.StdEncoding.DecodeString(got)
			require.NoError(t, err)
			require.Equal(t, "x-access-token:installation-token", string(decoded))
		}
	}
	require.Equal(t, 1, count)
	require.Equal(t, 1, keyCount)
	require.Equal(t, 1, valueCount)
}

func TestValidateRemoteOnlyConfiguresSSHForSSHURLs(t *testing.T) {
	require.True(t, usesSSH("git@github.com:org/repo.git"))
	require.True(t, usesSSH("ssh://git@github.com/org/repo.git"))
	require.False(t, usesSSH("https://github.com/org/repo.git"))
	require.False(t, usesSSH("file:///tmp/repo"))
}

func TestValidateRemoteEnvUsesSSHKeyOnlyForSSHRemote(t *testing.T) {
	manager := NewGitManager("/tmp/repo", "/tmp/private-key")
	require.Contains(t, manager.validateRemoteEnv("git@github.com:org/repo.git"), "GIT_SSH_COMMAND="+sshCommandFor("/tmp/private-key"))
	require.NotContains(t, manager.validateRemoteEnv("https://github.com/org/repo.git"), "GIT_SSH_COMMAND="+sshCommandFor("/tmp/private-key"))
}

func TestValidateRemoteEnvLeavesSSHAgentAvailableWithoutAKey(t *testing.T) {
	manager := NewGitManager("/tmp/repo", "")
	for _, item := range manager.validateRemoteEnv("git@github.com:org/repo.git") {
		require.NotContains(t, item, "GIT_SSH_COMMAND=")
	}
}

func TestHTTPTokenDoesNotConfigureSSH(t *testing.T) {
	manager := NewGitManager("/tmp/repo", "/tmp/ssh-key").WithHTTPToken("installation-token")
	for _, item := range manager.withGitEnv() {
		require.NotContains(t, item, "GIT_SSH_COMMAND=")
	}
}

func TestEmptySSHKeyDoesNotConfigureSSH(t *testing.T) {
	manager := NewGitManager("/tmp/repo", "")
	for _, item := range manager.withGitEnv() {
		require.NotContains(t, item, "GIT_SSH_COMMAND=")
	}
}

func TestCommitMessageIncludesHeadcount1CoAuthor(t *testing.T) {
	message := commitMessageWithHeadcount1Attribution("Fix repository discovery")
	require.Equal(t, "Fix repository discovery\n\n"+headcount1CoAuthorTrailer, message)
}

func TestCommitMessageDoesNotDuplicateHeadcount1CoAuthor(t *testing.T) {
	original := "Fix repository discovery\n\nco-authored-by: HEADCOUNT1.AI <HEADCOUNT1@HEADCOUNT1.AI>"
	require.Equal(t, original, commitMessageWithHeadcount1Attribution(original))
}

func TestCommitInWorktreeWritesHeadcount1CoAuthorTrailer(t *testing.T) {
	directory := t.TempDir()
	command := exec.Command("git", "init")
	command.Dir = directory
	require.NoError(t, command.Run())
	require.NoError(t, os.WriteFile(filepath.Join(directory, "change.txt"), []byte("change\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "memory.md"), []byte("task metadata\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", directory, "config", "user.name", "GitHub User").Run())
	require.NoError(t, exec.Command("git", "-C", directory, "config", "user.email", "github-user@example.com").Run())

	manager := NewGitManager(directory, "")
	require.NoError(t, manager.CommitInWorktree(context.Background(), directory, "Add a change"))

	command = exec.Command("git", "log", "-1", "--format=%B")
	command.Dir = directory
	message, err := command.Output()
	require.NoError(t, err)
	require.Contains(t, string(message), headcount1CoAuthorTrailer)
	require.Contains(t, string(message), "Co-authored-by: headcount1.ai <headcount1@headcount1.ai>")

	command = exec.Command("git", "-C", directory, "log", "-1", "--format=%an <%ae>")
	author, err := command.Output()
	require.NoError(t, err)
	require.Equal(t, "GitHub User <github-user@example.com>\n", string(author))

	command = exec.Command("git", "ls-tree", "--name-only", "HEAD", "memory.md")
	command.Dir = directory
	files, err := command.Output()
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(string(files)), "task memory must not be committed")
}

func TestGetStatusInDirIncludesUntrackedFiles(t *testing.T) {
	directory := t.TempDir()
	command := exec.Command("git", "init")
	command.Dir = directory
	require.NoError(t, command.Run())
	require.NoError(t, os.WriteFile(filepath.Join(directory, "untracked.txt"), []byte("change\n"), 0o644))

	status, err := NewGitManager(directory, "").GetStatusInDir(context.Background(), directory)
	require.NoError(t, err)
	require.Contains(t, status, "?? untracked.txt")
}

func TestValidateBranchName(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, ValidateBranchName(ctx, "release/2026.08"))
	require.Error(t, ValidateBranchName(ctx, "bad branch name"))
	require.Error(t, ValidateBranchName(ctx, ""))
}

func TestListRemoteBranches(t *testing.T) {
	directory := t.TempDir()
	command := exec.Command("git", "init")
	command.Dir = directory
	require.NoError(t, command.Run())
	initial := exec.Command("git", "-C", directory, "commit", "--allow-empty", "-m", "initial")
	initial.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2024-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2024-01-01T00:00:00Z")
	require.NoError(t, initial.Run())
	require.NoError(t, exec.Command("git", "-C", directory, "branch", "feature/base").Run())
	feature := exec.Command("git", "-C", directory, "checkout", "feature/base")
	require.NoError(t, feature.Run())
	newer := exec.Command("git", "-C", directory, "commit", "--allow-empty", "-m", "feature")
	newer.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2025-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2025-01-01T00:00:00Z")
	require.NoError(t, newer.Run())
	require.NoError(t, exec.Command("git", "-C", directory, "remote", "add", "origin", directory).Run())
	require.NoError(t, exec.Command("git", "-C", directory, "fetch", "origin").Run())
	currentBranch, err := exec.Command("git", "-C", directory, "branch", "--show-current").Output()
	require.NoError(t, err)

	branches, err := NewGitManager(directory, "").ListRemoteBranches(context.Background())
	require.NoError(t, err)
	require.Equal(t, "feature/base", branches[0])
	require.Contains(t, branches, strings.TrimSpace(string(currentBranch)))
	require.Contains(t, branches, "feature/base")
}
