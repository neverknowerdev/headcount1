package db

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetRunWithTaskPreloadsAgent(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&Company{}, &User{}, &Agent{}, &Sprint{}, &Task{}, &Run{}, &RunSnapshot{}))

	company := Company{Name: "Acme", ShortName: "acme"}
	require.NoError(t, database.Create(&company).Error)
	agent := Agent{CompanyID: company.ID, Name: "Orchestrator", RoleKey: "CEO", ShortName: "CEO", SystemPrompt: "You orchestrate."}
	require.NoError(t, database.Create(&agent).Error)
	sprint := Sprint{CompanyID: company.ID, Name: "Sprint"}
	require.NoError(t, database.Create(&sprint).Error)
	task := Task{CompanyID: company.ID, SprintID: sprint.ID, AgentID: &agent.ID, Title: "Task"}
	require.NoError(t, database.Create(&task).Error)
	run := Run{TaskID: task.ID, AgentID: agent.ID, Status: "completed"}
	require.NoError(t, database.Create(&run).Error)

	loaded, _, err := New(database).GetRunWithTask(context.Background(), run.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.Agent)
	require.Equal(t, agent.ID, loaded.Agent.ID)
	require.Equal(t, "CEO", loaded.Agent.RoleKey)
}

func TestRunResumeClaimIsAtomicAndPreservesCheckpoint(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&Company{}, &User{}, &Agent{}, &Sprint{}, &Task{}, &Run{}, &RunSnapshot{}))

	company := Company{Name: "Acme", ShortName: "acme"}
	require.NoError(t, database.Create(&company).Error)
	agent := Agent{CompanyID: company.ID, Name: "Runner", RoleKey: "CEO", ShortName: "CEO", SystemPrompt: "work"}
	require.NoError(t, database.Create(&agent).Error)
	task := Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "recover me"}
	require.NoError(t, database.Create(&task).Error)
	run := Run{TaskID: task.ID, AgentID: agent.ID, Status: RunStatusPaused}
	require.NoError(t, database.Create(&run).Error)
	snapshot := RunSnapshot{RunID: run.ID, CheckpointSequence: 2, CheckpointVersion: CheckpointVersion}
	require.NoError(t, database.Create(&snapshot).Error)

	q := New(database)
	lease := time.Now().Add(time.Minute)
	claimed, err := q.ClaimRunForResume(context.Background(), run.ID, "worker-1", "binary_update", run.Status, lease, []string{RunStatusPaused})
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = q.ClaimRunForResume(context.Background(), run.ID, "worker-2", "binary_update", run.Status, lease, []string{RunStatusPaused})
	require.NoError(t, err)
	require.False(t, claimed, "a second worker must not claim the same checkpoint")

	loaded, err := q.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, RunStatusResuming, loaded.Status)
	assert.Equal(t, snapshot.CheckpointSequence, loaded.Snapshot.CheckpointSequence)
	assert.Equal(t, 1, loaded.Snapshot.ResumeAttempts)

	require.NoError(t, q.MarkRunResumeStarted(context.Background(), run.ID, "worker-1"))
	loaded, err = q.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", loaded.Status)
	assert.Equal(t, snapshot.CheckpointSequence, loaded.Snapshot.CheckpointSequence, "checkpoint remains until terminal completion")
}

func TestRunResumeSupportsFailedAndStaleStatesAndLeaseRecovery(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&Company{}, &User{}, &Agent{}, &Sprint{}, &Task{}, &Run{}, &RunSnapshot{}))

	company := Company{Name: "Acme", ShortName: "acme"}
	require.NoError(t, database.Create(&company).Error)
	agent := Agent{CompanyID: company.ID, Name: "Runner", RoleKey: "CEO", ShortName: "CEO", SystemPrompt: "work"}
	require.NoError(t, database.Create(&agent).Error)
	task := Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "recover me"}
	require.NoError(t, database.Create(&task).Error)
	failed := Run{TaskID: task.ID, AgentID: agent.ID, Status: RunStatusRecoverableFailed}
	stale := Run{TaskID: task.ID, AgentID: agent.ID, Status: RunStatusStale}
	require.NoError(t, database.Create(&failed).Error)
	require.NoError(t, database.Create(&stale).Error)
	require.NoError(t, database.Create(&RunSnapshot{RunID: failed.ID, CheckpointSequence: 2, CheckpointVersion: CheckpointVersion}).Error)
	require.NoError(t, database.Create(&RunSnapshot{RunID: stale.ID, CheckpointSequence: 3, CheckpointVersion: CheckpointVersion}).Error)
	q := New(database)
	auto, err := q.GetRunsByRecoveryStates(context.Background(), []string{RunStatusPaused, RunStatusLegacyInterrupted})
	require.NoError(t, err)
	assert.Empty(t, auto, "failed and stale checkpoints are explicit-recovery only")

	claimed, err := q.ClaimRunForResume(context.Background(), failed.ID, "worker-f", "failed_recovery", failed.Status, time.Now().Add(time.Minute), []string{RunStatusRecoverableFailed})
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = q.ClaimRunForResume(context.Background(), stale.ID, "worker-s", "stale_recovery", stale.Status, time.Now().Add(time.Minute), []string{RunStatusStale})
	require.NoError(t, err)
	require.True(t, claimed)

	// Expiring both leases restores their original recovery policy instead of
	// converting failed/stale sessions into automatically resumed pauses.
	require.NoError(t, database.Model(&RunSnapshot{}).Where("run_id IN ?", []int32{failed.ID, stale.ID}).Updates(map[string]interface{}{"resume_lease_until": time.Now().Add(-time.Minute)}).Error)
	require.NoError(t, q.ReclaimExpiredResumeLeases(context.Background(), time.Now()))
	gotFailed, err := q.GetRun(context.Background(), failed.ID)
	require.NoError(t, err)
	gotStale, err := q.GetRun(context.Background(), stale.ID)
	require.NoError(t, err)
	assert.Equal(t, RunStatusRecoverableFailed, gotFailed.Status)
	assert.Equal(t, RunStatusStale, gotStale.Status)
	assert.NotZero(t, gotFailed.Snapshot.CheckpointSequence)
	assert.NotZero(t, gotStale.Snapshot.CheckpointSequence)
}

func TestMigrateRunSnapshotsMovesLegacyHistoryIntoJSONL(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&Run{}, &RunSnapshot{}))
	for _, column := range []string{
		"paused_history TEXT",
		"checkpoint_version INTEGER DEFAULT 0",
		"checkpoint_phase TEXT DEFAULT ''",
		"recovery_reason TEXT DEFAULT ''",
		"recovery_initiator TEXT DEFAULT ''",
		"recovery_target TEXT DEFAULT ''",
		"resume_previous_status TEXT DEFAULT ''",
		"resume_attempts INTEGER DEFAULT 0",
		"last_resume_error TEXT",
	} {
		require.NoError(t, database.Exec("ALTER TABLE runs ADD COLUMN "+column).Error)
	}
	logPath := filepath.Join(t.TempDir(), "run.jsonl")
	line, err := json.Marshal(map[string]interface{}{"type": "info", "seq": 4, "content": "legacy"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(logPath, append(line, '\n'), 0644))
	run := Run{Status: RunStatusPaused, LogFilePath: logPath}
	require.NoError(t, database.Create(&run).Error)
	history := `[{"role":"system","content":"system"},{"role":"assistant","content":"pending"}]`
	require.NoError(t, database.Exec("UPDATE runs SET paused_history = ?, checkpoint_phase = ?, recovery_reason = ? WHERE id = ?", history, "before_tools", "binary_update", run.ID).Error)

	q := New(database)
	require.NoError(t, q.MigrateRunSnapshots(context.Background()))
	loaded, err := q.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.Snapshot)
	assert.Equal(t, int64(6), loaded.Snapshot.CheckpointSequence)
	assert.Equal(t, "binary_update", loaded.Snapshot.RecoveryReason)

	// The migration is idempotent and must not append the legacy messages twice.
	require.NoError(t, q.MigrateRunSnapshots(context.Background()))
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, 3, len(splitNonEmptyLines(string(data))))
}

func splitNonEmptyLines(value string) []string {
	lines := []string{}
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
