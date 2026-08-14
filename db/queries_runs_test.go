package db

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
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

func TestRunEventInboxDeduplicatesAndConsumes(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&RunEvent{}))
	q := New(database)
	ctx := context.Background()
	event := RunEvent{TaskID: 7, RunID: 11, EventType: "run_status", Payload: "failed", DedupeKey: "run:11:failed:1"}
	require.NoError(t, q.EnqueueRunEvent(ctx, event))
	require.NoError(t, q.EnqueueRunEvent(ctx, event))
	pending, err := q.ListPendingRunEvents(ctx, 7)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, q.ConsumeRunEvents(ctx, []int64{pending[0].ID}))
	pending, err = q.ListPendingRunEvents(ctx, 7)
	require.NoError(t, err)
	require.Empty(t, pending)
}
