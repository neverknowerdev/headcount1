package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// NOTE: these tests deliberately only exercise Deploy paths that fail BEFORE it
// swaps the executable — a successful Deploy replaces os.Executable() (the test
// binary) and signals SIGTERM to its own process, which would kill the test run.
// The success path is covered end-to-end in e2e/tests/deploy_webhook.spec.ts
// against a real server process, where the exec-on-shutdown is the point.

func TestIsCurrent(t *testing.T) {
	u := New("v1.2.3", "main", "abc1234", "2026-01-01", nil)

	// Same commit → already current (a deploy event for it is a no-op).
	require.True(t, u.IsCurrent(VersionInfo{CommitHash: "abc1234"}))
	// Different commit → not current.
	require.False(t, u.IsCurrent(VersionInfo{CommitHash: "def5678"}))
	// An empty target commit is never "current" (avoids matching a dev build
	// whose own commit is also empty).
	require.False(t, u.IsCurrent(VersionInfo{}))
}

func TestDisplayString(t *testing.T) {
	// DisplayString stays the exact build identity even when a version number is
	// present: two builds can share a version, but never this triple.
	require.Equal(t, "main+2026-01-01+abc1234",
		VersionInfo{Version: "v1.2.3", Branch: "main", BuildDate: "2026-01-01", CommitHash: "abc1234"}.DisplayString())
	require.Equal(t, "dev", VersionInfo{}.DisplayString())
	require.Equal(t, "dev", VersionInfo{Branch: "x", CommitHash: "unknown"}.DisplayString())
}

// TestCurrentCarriesVersion pins that the version stamped in at build time is
// what the API and UI report, verbatim — production CalVer and staging names
// look nothing alike, and neither is parsed or reformatted anywhere.
func TestCurrentCarriesVersion(t *testing.T) {
	for _, version := range []string{"2026.07.29", "2026.07.29.2", "staging-my-branch-abc1234", "dev-abc1234"} {
		cur := New(version, "main", "abc1234", "2026-01-01", nil).Current()
		require.Equal(t, version, cur.Version)
		require.Equal(t, "abc1234", cur.CommitHash)
	}
}

// TestNewDoesNotArmRestart guards the invariant main relies on: a freshly
// started server must never think a deploy left a binary to exec.
func TestNewDoesNotArmRestart(t *testing.T) {
	_, pending := New("v1.2.3", "main", "abc1234", "2026-01-01", nil).RestartPending()
	require.False(t, pending)
}

// TestValidateDownloadURL covers the guard that keeps a leaked deploy key from
// becoming remote code execution: the binary must come from an allowed host and,
// on GitHub, from our own repository.
func TestValidateDownloadURL(t *testing.T) {
	const repo = "neverknowerdev/headcount1"

	valid := []string{
		"https://api.github.com/repos/" + repo + "/releases/assets/12345",
		"https://github.com/" + repo + "/releases/download/2026.07.29/agent-orchestrator-linux-amd64",
		// CDN hosts serve opaque signed paths and are only reached by redirect.
		"https://objects.githubusercontent.com/github-production-release-asset/abc",
	}
	for _, u := range valid {
		require.NoError(t, ValidateDownloadURL(u), "should accept %s", u)
	}

	invalid := map[string]string{
		"empty":          "",
		"attacker host":  "https://evil.example.com/backdoor",
		"http not https": "http://api.github.com/repos/" + repo + "/releases/assets/1",
		"other repo":     "https://api.github.com/repos/attacker/evil/releases/assets/1",
		"other repo web": "https://github.com/attacker/evil/releases/download/v1/bin",
		"host lookalike": "https://api.github.com.evil.example.com/repos/" + repo + "/x",
		"bad host chars": "https://exa mple.com/x",
	}
	for name, u := range invalid {
		require.Error(t, ValidateDownloadURL(u), "should reject %s (%s)", name, u)
	}
}

func TestValidateDownloadURLHostOverride(t *testing.T) {
	// An operator pointing at a private mirror may allow their own host, and
	// plain http for it; the GitHub repo-path pin doesn't apply to a mirror.
	t.Setenv("HEADCOUNT1_DEPLOY_ALLOWED_HOSTS", "mirror.internal, 127.0.0.1")
	require.NoError(t, ValidateDownloadURL("https://mirror.internal/builds/server"))
	require.NoError(t, ValidateDownloadURL("http://127.0.0.1:9000/server"))
	// Hosts outside the override are still refused, including the GitHub
	// defaults it replaced.
	require.Error(t, ValidateDownloadURL("https://api.github.com/repos/x/y/releases/assets/1"))
	require.Error(t, ValidateDownloadURL("https://evil.example.com/backdoor"))
}

func TestValidateDownloadURLCustomRepo(t *testing.T) {
	t.Setenv("HEADCOUNT1_DEPLOY_REPO", "someone/otherapp")
	require.NoError(t, ValidateDownloadURL("https://api.github.com/repos/someone/otherapp/releases/assets/1"))
	require.Error(t, ValidateDownloadURL("https://api.github.com/repos/neverknowerdev/headcount1/releases/assets/1"))
}

func TestDeployRejectsDisallowedHost(t *testing.T) {
	u := New("v1.2.3", "main", "abc1234", "2026-01-01", nil)
	target := VersionInfo{CommitHash: "def5678"}

	// Fails before anything is fetched, let alone swapped.
	require.Error(t, u.Deploy("https://evil.example.com/backdoor", "deadbeef", target))
	require.False(t, u.GetStatus().Deploying, "a failed deploy must not stay wedged as deploying")
	require.NotEmpty(t, u.GetStatus().LastError)
	_, pending := u.RestartPending()
	require.False(t, pending)
}

func TestDeployRequiresDigest(t *testing.T) {
	t.Setenv("HEADCOUNT1_DEPLOY_ALLOWED_HOSTS", "127.0.0.1")
	u := New("v1.2.3", "main", "abc1234", "2026-01-01", nil)

	// Without a digest any published artifact — including an old, vulnerable
	// build — would be a valid deploy target, so it is refused outright.
	err := u.Deploy("http://127.0.0.1:1/server", "", VersionInfo{CommitHash: "def5678"})
	require.ErrorContains(t, err, "sha256 is required")
	require.False(t, u.GetStatus().Deploying)
	_, pending := u.RestartPending()
	require.False(t, pending)
}

// TestDeployRejectsDigestMismatch proves the downloaded bytes are actually
// verified: a served binary that doesn't match the advertised digest is
// discarded, no restart is armed, and the running executable is left untouched.
func TestDeployRejectsDigestMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("#!/bin/sh\necho not-the-real-binary\n"))
	}))
	defer srv.Close()

	t.Setenv("HEADCOUNT1_DEPLOY_ALLOWED_HOSTS", "127.0.0.1")

	u := New("v1.2.3", "main", "abc1234", "2026-01-01", nil)
	emptySum := sha256.Sum256(nil) // a digest the served body cannot match
	err := u.Deploy(srv.URL+"/server", hex.EncodeToString(emptySum[:]), VersionInfo{CommitHash: "def5678"})

	require.ErrorContains(t, err, "sha256 mismatch")
	_, pending := u.RestartPending()
	require.False(t, pending, "a rejected binary must not arm a restart")
	require.False(t, u.GetStatus().Deploying)
}
