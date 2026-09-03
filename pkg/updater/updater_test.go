package updater

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVerifyArtifactSignatureBindsEnvelope(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	target := VersionInfo{Version: "v2", Branch: "main", CommitHash: "abc", BuildDate: "today"}
	message := ArtifactSigningMessage("https://github.com/neverknowerdev/headcount1/releases/x", "deadbeef", target)
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(message)))
	require.NoError(t, VerifyArtifactSignature("https://github.com/neverknowerdev/headcount1/releases/x", "deadbeef", target, signature, base64.StdEncoding.EncodeToString(publicKey)))
	require.Error(t, VerifyArtifactSignature("https://github.com/neverknowerdev/headcount1/releases/y", "deadbeef", target, signature, base64.StdEncoding.EncodeToString(publicKey)))
}

func TestDeploymentStateRoundTripAndStartupFallback(t *testing.T) {
	basePath := t.TempDir()
	previous := filepath.Join(basePath, "releases", "previous", "agent-orchestrator")
	state := DeploymentState{
		ID: "abc-123", Phase: DeployPhaseMigrating,
		Target:        VersionInfo{Version: "v2", Branch: "main", CommitHash: "new"},
		CandidatePath: filepath.Join(basePath, "releases", "new", "agent-orchestrator"),
		PreviousPath:  previous, ArtifactSHA256: "digest", StartedAt: time.Now().UTC(),
	}
	require.NoError(t, SaveState(basePath, state))
	loaded, err := LoadState(basePath)
	require.NoError(t, err)
	require.Equal(t, state.ID, loaded.ID)
	require.Equal(t, state.CandidatePath, loaded.CandidatePath)

	u := NewWithBasePath("v2", "main", "new", "today", nil, basePath)
	fallback, err := u.RecordStartupFailure(VersionInfo{CommitHash: "new"}, errors.New("migration 2 failed: bad sql"))
	require.NoError(t, err)
	require.Equal(t, previous, fallback)
	failed, found, err := u.DeploymentState(state.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, DeployPhaseFailed, failed.Phase)
	require.Contains(t, failed.LastError, "bad sql")

	reloaded := NewWithBasePath("v1", "main", "old", "today", nil, basePath)
	require.NoError(t, reloaded.MarkMigrating())
	require.NoError(t, reloaded.MarkStarting())
	require.NoError(t, reloaded.MarkPromoted())
	preserved, found, err := reloaded.DeploymentState(state.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, DeployPhaseFailed, preserved.Phase, "fallback binary must preserve the failed candidate journal")
	require.Equal(t, string(DeployPhaseFailed), reloaded.GetStatus().Phase)
	require.Contains(t, reloaded.GetStatus().LastError, "bad sql")
}

func TestDeploymentStateDistinguishesStartingAndManualRecovery(t *testing.T) {
	basePath := t.TempDir()
	state := DeploymentState{
		ID: "manual-1", Phase: DeployPhaseStaged,
		Target:        VersionInfo{CommitHash: "new"},
		CandidatePath: filepath.Join(basePath, "candidate"),
		PreviousPath:  filepath.Join(basePath, "previous"),
	}
	require.NoError(t, SaveState(basePath, state))

	u := NewWithBasePath("v2", "main", "new", "today", nil, basePath)
	require.NoError(t, u.MarkStarting())
	starting, found, err := u.DeploymentState(state.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, DeployPhaseStarting, starting.Phase)
	require.True(t, u.GetStatus().Deploying)

	require.NoError(t, u.MarkNeedsManualRecovery(VersionInfo{CommitHash: "new"}, errors.New("rollback failed")))
	manual, found, err := u.DeploymentState(state.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, DeployPhaseNeedsManualRecovery, manual.Phase)
	require.Contains(t, manual.LastError, "rollback failed")
	require.False(t, u.GetStatus().Deploying)
}

func TestMarkPromotedRetainsCurrentAndTenPreviousBinaries(t *testing.T) {
	basePath := t.TempDir()
	releasesRoot := filepath.Join(basePath, "releases")
	var previous []ReleaseRecord
	for i := 0; i < 13; i++ {
		id := fmt.Sprintf("release-%02d", i)
		path := filepath.Join(releasesRoot, id, "agent-orchestrator")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte(id), 0755))
		previous = append(previous, ReleaseRecord{
			ID: id, Target: VersionInfo{CommitHash: id}, Path: path,
			PromotedAt: time.Now().UTC(),
		})
	}
	currentPath := filepath.Join(releasesRoot, "release-current", "agent-orchestrator")
	require.NoError(t, os.MkdirAll(filepath.Dir(currentPath), 0755))
	require.NoError(t, os.WriteFile(currentPath, []byte("current"), 0755))

	state := DeploymentState{
		ID: "release-current", Phase: DeployPhaseStarting,
		Target: VersionInfo{CommitHash: "release-current"}, CandidatePath: currentPath,
		PreviousPath: previous[len(previous)-1].Path, SuccessfulReleases: previous,
	}
	require.NoError(t, SaveState(basePath, state))

	u := NewWithBasePath("v-current", "main", "release-current", "today", nil, basePath)
	require.NoError(t, u.MarkPromoted())

	for i := 0; i < 2; i++ {
		_, err := os.Stat(previous[i].Path)
		require.ErrorIs(t, err, os.ErrNotExist, "release %s should be pruned", previous[i].ID)
	}
	for i := 2; i < len(previous); i++ {
		_, err := os.Stat(previous[i].Path)
		require.NoError(t, err, "release %s should be retained", previous[i].ID)
	}
	_, err := os.Stat(currentPath)
	require.NoError(t, err, "current binary should be retained")

	saved, err := LoadState(basePath)
	require.NoError(t, err)
	require.Len(t, saved.SuccessfulReleases, successfulReleaseRetention)
	require.Equal(t, "release-current", saved.SuccessfulReleases[len(saved.SuccessfulReleases)-1].ID)

	failedPath := filepath.Join(releasesRoot, "release-failed", "agent-orchestrator")
	require.NoError(t, os.MkdirAll(filepath.Dir(failedPath), 0755))
	require.NoError(t, os.WriteFile(failedPath, []byte("failed"), 0755))
	saved.ID = "release-failed"
	saved.Phase = DeployPhaseFailed
	saved.Target = VersionInfo{CommitHash: "release-failed"}
	saved.CandidatePath = failedPath
	saved.PreviousPath = currentPath
	require.NoError(t, SaveState(basePath, saved))
	_ = NewWithBasePath("v-failed", "main", "release-failed", "today", nil, basePath)
	_, err = os.Stat(failedPath)
	require.ErrorIs(t, err, os.ErrNotExist, "failed candidate should be pruned when fallback boots")
}

// NOTE: these unit tests avoid the final SIGTERM/exec path. Candidate staging,
// durable state, and the full restart are covered by the deployment E2E suite.

func TestIsCurrent(t *testing.T) {
	u := NewWithBasePath("v1.2.3", "main", "abc1234", "2026-01-01", nil, t.TempDir())

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
		// Git tags may contain slashes, so an attacker's release tag can spell
		// out our repo name mid-path. Only a prefix-anchored pin refuses these.
		"repo name in tag":     "https://github.com/attacker/evil/releases/download/" + repo + "/bin",
		"repo name in api tag": "https://api.github.com/repos/attacker/evil/releases/tags/" + repo + "/x",
		"host lookalike":       "https://api.github.com.evil.example.com/repos/" + repo + "/x",
		"bad host chars":       "https://exa mple.com/x",
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

	u := NewWithBasePath("v1.2.3", "main", "abc1234", "2026-01-01", nil, t.TempDir())
	emptySum := sha256.Sum256(nil) // a digest the served body cannot match
	err := u.Deploy(srv.URL+"/server", hex.EncodeToString(emptySum[:]), VersionInfo{CommitHash: "def5678"})

	require.ErrorContains(t, err, "sha256 mismatch")
	_, pending := u.RestartPending()
	require.False(t, pending, "a rejected binary must not arm a restart")
	require.False(t, u.GetStatus().Deploying)
}
