package engine_test

import (
	"context"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"

	"github.com/stretchr/testify/require"
)

func TestProcessTaskMovesQueuedWorkToDependsOnTask(t *testing.T) {
	database := setupTestDB(t)
	task := seedTestData(t, database, "")
	q := db.New(database)
	ctx := context.Background()

	prerequisite := db.Task{CompanyID: task.CompanyID, SprintID: task.SprintID, Title: "Prerequisite", Status: db.TaskStatusInProgress}
	created, err := q.CreateTask(ctx, prerequisite)
	require.NoError(t, err)
	require.NoError(t, database.Model(&db.Task{}).Where("id = ?", task.ID).Update("status", db.TaskStatusTodo).Error)
	_, err = q.CreateTaskRelation(ctx, db.TaskRelation{CompanyID: task.CompanyID, SourceTaskID: task.ID, TargetTaskID: created.ID, Kind: db.TaskRelationDependsOn})
	require.NoError(t, err)

	eng := engine.NewNativeEngine(database, eventhub.NewHub())
	require.NoError(t, eng.ProcessTask(ctx, task.ID))

	updated, err := q.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, db.TaskStatusDependsOnTask, updated.Status)
	var runs int64
	require.NoError(t, database.Model(&db.Run{}).Where("task_id = ?", task.ID).Count(&runs).Error)
	require.Zero(t, runs)
}

func TestRerunTaskReturnsDependencyBlockers(t *testing.T) {
	database := setupTestDB(t)
	task := seedTestData(t, database, "")
	q := db.New(database)
	ctx := context.Background()
	prerequisite, err := q.CreateTask(ctx, db.Task{CompanyID: task.CompanyID, SprintID: task.SprintID, Title: "Prerequisite", Status: db.TaskStatusInProgress})
	require.NoError(t, err)
	require.NoError(t, database.Model(&db.Task{}).Where("id = ?", task.ID).Update("status", db.TaskStatusDependsOnTask).Error)
	_, err = q.CreateTaskRelation(ctx, db.TaskRelation{CompanyID: task.CompanyID, SourceTaskID: task.ID, TargetTaskID: prerequisite.ID, Kind: db.TaskRelationDependsOn})
	require.NoError(t, err)

	eng := engine.NewNativeEngine(database, eventhub.NewHub())
	err = eng.RerunTask(ctx, task.ID)
	var blocked *engine.TaskDependencyBlockedError
	require.ErrorAs(t, err, &blocked)
	require.Equal(t, task.ID, blocked.TaskID)
	require.Len(t, blocked.Blockers, 1)
}
