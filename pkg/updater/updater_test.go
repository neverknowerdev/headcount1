package updater

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsCurrent(t *testing.T) {
	u := New("main", "abc1234", "2026-01-01", nil)

	// Same commit → already current (a deploy event for it is a no-op).
	require.True(t, u.IsCurrent(VersionInfo{CommitHash: "abc1234"}))
	// Different commit → not current.
	require.False(t, u.IsCurrent(VersionInfo{CommitHash: "def5678"}))
	// An empty target commit is never "current" (avoids matching a dev build
	// whose own commit is also empty).
	require.False(t, u.IsCurrent(VersionInfo{}))
}

func TestDeployRejectsEmptyURL(t *testing.T) {
	u := New("main", "abc1234", "2026-01-01", nil)
	require.Error(t, u.Deploy("", VersionInfo{CommitHash: "def5678"}))
	// The failed deploy must not leave the updater wedged as "deploying".
	require.False(t, u.GetStatus().Deploying)
	require.NotEmpty(t, u.GetStatus().LastError)
}

func TestDisplayString(t *testing.T) {
	require.Equal(t, "main+2026-01-01+abc1234",
		VersionInfo{Branch: "main", BuildDate: "2026-01-01", CommitHash: "abc1234"}.DisplayString())
	require.Equal(t, "dev", VersionInfo{}.DisplayString())
	require.Equal(t, "dev", VersionInfo{Branch: "x", CommitHash: "unknown"}.DisplayString())
}
