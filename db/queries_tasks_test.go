package db

import (
	"agent-orchestrator/db/migrations"
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreateTaskAssignsHumanReadableSharedBranch(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))

	creator := User{Email: "owner@example.com"}
	require.NoError(t, database.Create(&creator).Error)
	company := Company{Name: "HeadCount1", ShortName: "hc1", UserID: &creator.ID}
	require.NoError(t, database.Create(&company).Error)
	sprint := Sprint{CompanyID: company.ID, Name: "Sprint 1"}
	require.NoError(t, database.Create(&sprint).Error)

	q := New(database)
	root, err := q.CreateTask(context.Background(), Task{
		CompanyID: company.ID,
		SprintID:  sprint.ID,
		Title:     "Deployment settings",
	})
	require.NoError(t, err)
	require.Equal(t, "HC1-"+strconv.Itoa(int(root.ID)), root.RefKey)
	require.Equal(t, "headcount1/HC1-"+strconv.Itoa(int(root.ID)), root.GitHubBranch)

	child, err := q.CreateTask(context.Background(), Task{
		CompanyID: company.ID,
		SprintID:  sprint.ID,
		ParentID:  &root.ID,
		Title:     "Implement backend",
	})
	require.NoError(t, err)
	require.Equal(t, root.GitHubBranch, child.GitHubBranch)
}

func TestTaskDoneAtTracksDoneTransitions(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := Company{Name: "HeadCount1", ShortName: "hc1"}
	require.NoError(t, database.Create(&company).Error)
	q := New(database)
	task, err := q.CreateTask(context.Background(), Task{CompanyID: company.ID, Title: "retained workspace", Status: TaskStatusInProgress})
	require.NoError(t, err)

	task.Status = TaskStatusDone
	_, err = q.UpdateTask(context.Background(), task)
	require.NoError(t, err)
	done, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.NotNil(t, done.DoneAt)
	require.WithinDuration(t, time.Now(), *done.DoneAt, 2*time.Second)
	doneAt := *done.DoneAt
	_, err = q.UpdateTask(context.Background(), Task{ID: task.ID, CompanyID: company.ID, Title: task.Title, Status: TaskStatusDone})
	require.NoError(t, err)
	persisted, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.DoneAt)
	require.Equal(t, doneAt, *persisted.DoneAt)

	changed, err := q.SetTaskStatusIf(context.Background(), task.ID, TaskStatusDone, TaskStatusInProgress)
	require.NoError(t, err)
	require.True(t, changed)
	active, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Nil(t, active.DoneAt)

	changed, err = q.SetTaskStatusIf(context.Background(), task.ID, TaskStatusInProgress, TaskStatusDone)
	require.NoError(t, err)
	require.True(t, changed)
	finished, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.NotNil(t, finished.DoneAt)
}
