package db

import (
	"agent-orchestrator/db/migrations"
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
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))

	company := Company{Name: "Acme", ShortName: "acme"}
	require.NoError(t, database.Create(&company).Error)
	agent := Agent{CompanyID: company.ID, Name: "CEO", RoleKey: "CEO", ShortName: "CEO", SystemPrompt: "You are the CEO agent."}
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

func TestRunRecoveryIsOneSerializedDocument(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	require.True(t, database.Migrator().HasColumn(&Run{}, "recovery"))
	for _, legacyColumn := range []string{
		"checkpoint_sequence", "checkpoint_version", "checkpoint_phase",
		"recovery_reason", "recovery_initiator", "recovery_target",
		"resume_lease_owner", "resume_lease_until", "resume_previous_status",
		"resume_attempts", "last_resume_error",
	} {
		assert.False(t, database.Migrator().HasColumn(&Run{}, legacyColumn), "legacy recovery column %s must not be part of the current schema", legacyColumn)
	}
	run := Run{
		TaskID: 1, AgentID: 1, Status: RunStatusPaused,
		Recovery: RunRecovery{
			CheckpointSequence: 9, CheckpointVersion: CheckpointVersion,
			CheckpointPhase: CheckpointPhaseBeforeTools, RecoveryReason: "restart",
			ResumeAttempts: 2,
		},
	}
	require.NoError(t, database.Create(&run).Error)
	var raw string
	require.NoError(t, database.Raw("SELECT recovery FROM runs WHERE id = ?", run.ID).Scan(&raw).Error)
	assert.Contains(t, raw, `"checkpoint_sequence":9`)
	var loaded Run
	require.NoError(t, database.First(&loaded, run.ID).Error)
	assert.Equal(t, run.Recovery, loaded.Recovery)
}

func TestRunResumeClaimIsAtomicAndPreservesCheckpoint(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))

	company := Company{Name: "Acme", ShortName: "acme"}
	require.NoError(t, database.Create(&company).Error)
	agent := Agent{CompanyID: company.ID, Name: "Runner", RoleKey: "CEO", ShortName: "CEO", SystemPrompt: "work"}
	require.NoError(t, database.Create(&agent).Error)
	task := Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "recover me"}
	require.NoError(t, database.Create(&task).Error)
	run := Run{TaskID: task.ID, AgentID: agent.ID, Status: RunStatusPaused, Recovery: RunRecovery{CheckpointSequence: 2, CheckpointVersion: CheckpointVersion}}
	require.NoError(t, database.Create(&run).Error)

	q := New(database)
	lease := time.Now().Add(time.Minute)
	claimed, err := q.ClaimRunForResume(context.Background(), run.ID, "worker-1", "binary_update", run.Status, lease, []string{RunStatusPaused}, run.Recovery.CheckpointSequence)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = q.ClaimRunForResume(context.Background(), run.ID, "worker-2", "binary_update", run.Status, lease, []string{RunStatusPaused}, run.Recovery.CheckpointSequence)
	require.NoError(t, err)
	require.False(t, claimed, "a second worker must not claim the same checkpoint")

	loaded, err := q.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, RunStatusResuming, loaded.Status)
	assert.Equal(t, int64(2), loaded.Recovery.CheckpointSequence)
	assert.Equal(t, 1, loaded.Recovery.ResumeAttempts)

	require.NoError(t, q.MarkRunResumeStarted(context.Background(), run.ID, "worker-1"))
	loaded, err = q.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", loaded.Status)
	assert.Equal(t, int64(2), loaded.Recovery.CheckpointSequence, "checkpoint remains until terminal completion")
}

// A worker that claims a paused session and dies before starting it must not
// strand the session: once its lease runs out the session is paused again,
// checkpoint intact, for the next worker to claim.
func TestExpiredResumeLeaseReturnsRunToPaused(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))

	company := Company{Name: "Acme", ShortName: "acme"}
	require.NoError(t, database.Create(&company).Error)
	agent := Agent{CompanyID: company.ID, Name: "Runner", RoleKey: "Coder", ShortName: "CODER", SystemPrompt: "work"}
	require.NoError(t, database.Create(&agent).Error)
	task := Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "recover me"}
	require.NoError(t, database.Create(&task).Error)
	run := Run{TaskID: task.ID, AgentID: agent.ID, Status: RunStatusPaused, Recovery: RunRecovery{CheckpointSequence: 2, CheckpointVersion: CheckpointVersion}}
	require.NoError(t, database.Create(&run).Error)

	q := New(database)
	ctx := context.Background()
	claimed, err := q.ClaimRunForResume(ctx, run.ID, "worker-1", "restart", run.Status, time.Now().Add(time.Minute), []string{RunStatusPaused}, 2)
	require.NoError(t, err)
	require.True(t, claimed)

	// A lease that has not run out is left alone.
	require.NoError(t, q.ReclaimExpiredResumeLeases(ctx, time.Now()))
	held, err := q.GetRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, RunStatusResuming, held.Status)

	require.NoError(t, q.ReclaimExpiredResumeLeases(ctx, time.Now().Add(2*time.Minute)))
	reclaimed, err := q.GetRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, RunStatusPaused, reclaimed.Status)
	assert.Equal(t, int64(2), reclaimed.Recovery.CheckpointSequence)
	assert.Empty(t, reclaimed.Recovery.ResumeLeaseOwner)

	paused, err := q.GetRunsByRecoveryStates(ctx, []string{RunStatusPaused})
	require.NoError(t, err)
	require.Len(t, paused, 1)
	claimed, err = q.ClaimRunForResume(ctx, run.ID, "worker-2", "restart", reclaimed.Status, time.Now().Add(time.Minute), []string{RunStatusPaused}, 2)
	require.NoError(t, err)
	assert.True(t, claimed, "the session can be claimed again after its lease expired")
}
