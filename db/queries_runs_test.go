package db

import (
	"context"
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
	require.NoError(t, database.AutoMigrate(&Company{}, &User{}, &Agent{}, &Sprint{}, &Task{}, &Run{}))

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
	require.NoError(t, database.AutoMigrate(&Company{}, &User{}, &Agent{}, &Sprint{}, &Task{}, &Run{}))

	company := Company{Name: "Acme", ShortName: "acme"}
	require.NoError(t, database.Create(&company).Error)
	agent := Agent{CompanyID: company.ID, Name: "Runner", RoleKey: "CEO", ShortName: "CEO", SystemPrompt: "work"}
	require.NoError(t, database.Create(&agent).Error)
	task := Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "recover me"}
	require.NoError(t, database.Create(&task).Error)
	run := Run{TaskID: task.ID, AgentID: agent.ID, Status: RunStatusPaused, CheckpointSequence: 2, CheckpointVersion: CheckpointVersion}
	require.NoError(t, database.Create(&run).Error)

	q := New(database)
	lease := time.Now().Add(time.Minute)
	claimed, err := q.ClaimRunForResume(context.Background(), run.ID, "worker-1", "binary_update", run.Status, lease, []string{RunStatusPaused}, run.CheckpointSequence)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = q.ClaimRunForResume(context.Background(), run.ID, "worker-2", "binary_update", run.Status, lease, []string{RunStatusPaused}, run.CheckpointSequence)
	require.NoError(t, err)
	require.False(t, claimed, "a second worker must not claim the same checkpoint")

	loaded, err := q.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, RunStatusResuming, loaded.Status)
	assert.Equal(t, int64(2), loaded.CheckpointSequence)
	assert.Equal(t, 1, loaded.ResumeAttempts)

	require.NoError(t, q.MarkRunResumeStarted(context.Background(), run.ID, "worker-1"))
	loaded, err = q.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", loaded.Status)
	assert.Equal(t, int64(2), loaded.CheckpointSequence, "checkpoint remains until terminal completion")
}

func TestRunResumeSupportsFailedAndStaleStatesAndLeaseRecovery(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&Company{}, &User{}, &Agent{}, &Sprint{}, &Task{}, &Run{}))

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
	require.NoError(t, database.Model(&Run{}).Where("id = ?", failed.ID).Updates(map[string]interface{}{"checkpoint_sequence": 2, "checkpoint_version": CheckpointVersion}).Error)
	require.NoError(t, database.Model(&Run{}).Where("id = ?", stale.ID).Updates(map[string]interface{}{"checkpoint_sequence": 3, "checkpoint_version": CheckpointVersion}).Error)
	q := New(database)
	auto, err := q.GetRunsByRecoveryStates(context.Background(), []string{RunStatusPaused, RunStatusLegacyInterrupted})
	require.NoError(t, err)
	assert.Empty(t, auto, "failed and stale checkpoints are explicit-recovery only")

	claimed, err := q.ClaimRunForResume(context.Background(), failed.ID, "worker-f", "failed_recovery", failed.Status, time.Now().Add(time.Minute), []string{RunStatusRecoverableFailed}, 2)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = q.ClaimRunForResume(context.Background(), stale.ID, "worker-s", "stale_recovery", stale.Status, time.Now().Add(time.Minute), []string{RunStatusStale}, 3)
	require.NoError(t, err)
	require.True(t, claimed)

	// Expiring both leases restores their original recovery policy instead of
	// converting failed/stale sessions into automatically resumed pauses.
	require.NoError(t, database.Model(&Run{}).Where("id IN ?", []int32{failed.ID, stale.ID}).Updates(map[string]interface{}{"resume_lease_until": time.Now().Add(-time.Minute)}).Error)
	require.NoError(t, q.ReclaimExpiredResumeLeases(context.Background(), time.Now()))
	gotFailed, err := q.GetRun(context.Background(), failed.ID)
	require.NoError(t, err)
	gotStale, err := q.GetRun(context.Background(), stale.ID)
	require.NoError(t, err)
	assert.Equal(t, RunStatusRecoverableFailed, gotFailed.Status)
	assert.Equal(t, RunStatusStale, gotStale.Status)
	assert.NotZero(t, gotFailed.CheckpointSequence)
	assert.NotZero(t, gotStale.CheckpointSequence)
}

func TestMarkRunStaleIsAtomicAndTerminal(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&Run{}))
	run := Run{TaskID: 1, AgentID: 1, Status: "running", StartedAt: time.Now().Add(-time.Hour)}
	require.NoError(t, database.Create(&run).Error)
	q := New(database)
	changed, err := q.MarkRunStale(context.Background(), run.ID, "heartbeat timeout")
	require.NoError(t, err)
	assert.True(t, changed)
	changed, err = q.MarkRunStale(context.Background(), run.ID, "duplicate monitor tick")
	require.NoError(t, err)
	assert.False(t, changed)
	loaded, err := q.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, RunStatusStale, loaded.Status)
	assert.NotNil(t, loaded.EndedAt)
	assert.Equal(t, "heartbeat timeout", loaded.RecoveryReason)
}

func TestMigrateRunRecoveryToRunsDropsLegacyTable(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&Run{}))
	run := Run{TaskID: 1, AgentID: 1, Status: RunStatusPaused}
	require.NoError(t, database.Create(&run).Error)
	require.NoError(t, database.Exec(`CREATE TABLE run_snapshots (
		run_id integer primary key, checkpoint_sequence integer, checkpoint_version integer,
		checkpoint_phase text, recovery_reason text, recovery_initiator text, recovery_target text,
		resume_lease_owner text, resume_lease_until datetime, resume_previous_status text,
		resume_attempts integer, last_resume_error text
	)`).Error)
	require.NoError(t, database.Exec(`INSERT INTO run_snapshots (run_id, checkpoint_sequence, checkpoint_version, checkpoint_phase, recovery_reason, resume_attempts) VALUES (?, ?, ?, ?, ?, ?)`, run.ID, 7, 1, "before_tools", "legacy", 2).Error)
	require.NoError(t, MigrateRunRecoveryToRuns(database))
	loaded, err := New(database).GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(7), loaded.CheckpointSequence)
	assert.Equal(t, CheckpointVersion, loaded.CheckpointVersion)
	assert.Equal(t, "legacy", loaded.RecoveryReason)
	assert.Equal(t, 2, loaded.ResumeAttempts)
	assert.False(t, database.Migrator().HasTable("run_snapshots"))
}
