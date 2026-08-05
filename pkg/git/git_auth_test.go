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
	original := "Fix repository discovery\n\nco-authored-by: HEADCOUNT1.IO <HEADCOUNT1@HEADCOUNT1.IO>"
	require.Equal(t, original, commitMessageWithHeadcount1Attribution(original))
}

func TestCommitInWorktreeWritesHeadcount1CoAuthorTrailer(t *testing.T) {
	directory := t.TempDir()
	command := exec.Command("git", "init")
	command.Dir = directory
	require.NoError(t, command.Run())
	require.NoError(t, os.WriteFile(filepath.Join(directory, "change.txt"), []byte("change\n"), 0o644))

	manager := NewGitManager(directory, "")
	require.NoError(t, manager.CommitInWorktree(context.Background(), directory, "Add a change"))

	command = exec.Command("git", "log", "-1", "--format=%B")
	command.Dir = directory
	message, err := command.Output()
	require.NoError(t, err)
	require.Contains(t, string(message), headcount1CoAuthorTrailer)
}
