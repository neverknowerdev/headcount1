package git

import (
	"encoding/base64"
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
