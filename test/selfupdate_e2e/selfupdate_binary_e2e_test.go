package selfupdate_e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/pkg/updater"
	"github.com/stretchr/testify/require"
)

// This is deliberately a process-level suite, not a second GitHub workflow.
// It exercises the stable-parent/child exit contract without requiring a
// release asset or a live database, and is run from the existing E2E workflow.
func TestSupervisorPromotesCandidateAfterUpdateExit(t *testing.T) {
	basePath := t.TempDir()
	// RunSupervisor inherits these helper variables and launches the helper via
	// the -test.run selector. The first child stages then requests hand-off;
	// the second child is marked candidate and exits successfully.
	// The helper selector is passed as an argument; environment is inherited.
	err := updater.RunSupervisorWithEnv(context.Background(), os.Args[0], []string{"-test.run", "TestSelfUpdateHelper"}, basePath, []string{"HEADCOUNT1_SELFUPDATE_HELPER=1", "HEADCOUNT1_SELFUPDATE_BASE=" + basePath})
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(basePath, "candidate.started"))
	require.NoError(t, err)
}

func TestSupervisorRestoresPreviousAfterCandidateFailure(t *testing.T) {
	basePath := t.TempDir()
	err := updater.RunSupervisorWithEnv(context.Background(), os.Args[0], []string{"-test.run", "TestSelfUpdateFailureHelper"}, basePath, []string{
		"HEADCOUNT1_SELFUPDATE_HELPER=1", "HEADCOUNT1_SELFUPDATE_FAIL=1", "HEADCOUNT1_SELFUPDATE_BASE=" + basePath,
	})
	require.NoError(t, err)
	state, err := updater.LoadState(basePath)
	require.NoError(t, err)
	require.Equal(t, updater.DeployPhaseFailed, state.Phase)
	require.NotEmpty(t, state.LastError)
}

func TestSelfUpdateHelper(t *testing.T) {
	if os.Getenv("HEADCOUNT1_SELFUPDATE_HELPER") != "1" {
		return
	}
	basePath := os.Getenv("HEADCOUNT1_SELFUPDATE_BASE")
	if os.Getenv("HEADCOUNT1_SUPERVISOR_CANDIDATE") == "1" {
		if err := os.WriteFile(filepath.Join(basePath, "candidate.started"), []byte("ok"), 0600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	executable, _ := os.Executable()
	_ = updater.SaveState(basePath, updater.DeploymentState{
		ID: "e2e", Phase: updater.DeployPhaseStaged, CandidatePath: executable,
		PreviousPath: executable, Target: updater.VersionInfo{CommitHash: "candidate"},
	})
	os.Exit(updater.UpdateRequestedExitCode)
}

func TestSelfUpdateFailureHelper(t *testing.T) {
	if os.Getenv("HEADCOUNT1_SELFUPDATE_HELPER") != "1" {
		return
	}
	basePath := os.Getenv("HEADCOUNT1_SELFUPDATE_BASE")
	if os.Getenv("HEADCOUNT1_SUPERVISOR_CANDIDATE") == "1" {
		os.Exit(1)
	}
	if _, err := os.Stat(filepath.Join(basePath, "attempted")); err == nil {
		os.Exit(0)
	}
	_ = os.WriteFile(filepath.Join(basePath, "attempted"), []byte("ok"), 0600)
	executable, _ := os.Executable()
	_ = updater.SaveState(basePath, updater.DeploymentState{ID: "failed", Phase: updater.DeployPhaseStaged, CandidatePath: executable, PreviousPath: executable, Target: updater.VersionInfo{CommitHash: "candidate"}})
	os.Exit(updater.UpdateRequestedExitCode)
}
