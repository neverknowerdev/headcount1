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

	_, err = q.UpdateTaskFields(context.Background(), task.ID, map[string]interface{}{"status": TaskStatusDone})
	require.NoError(t, err)
	done, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.NotNil(t, done.DoneAt)
	require.WithinDuration(t, time.Now(), *done.DoneAt, 2*time.Second)
	doneAt := *done.DoneAt
	// Writing done again while it is already done keeps the original time.
	_, err = q.UpdateTaskFields(context.Background(), task.ID, map[string]interface{}{"status": TaskStatusDone, "title": "still done"})
	require.NoError(t, err)
	persisted, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.DoneAt)
	require.Equal(t, doneAt, *persisted.DoneAt)

	_, err = q.UpdateTaskFields(context.Background(), task.ID, map[string]interface{}{"status": TaskStatusInProgress})
	require.NoError(t, err)
	active, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Nil(t, active.DoneAt)

	_, err = q.UpdateTaskFields(context.Background(), task.ID, map[string]interface{}{"status": TaskStatusDone})
	require.NoError(t, err)
	finished, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.NotNil(t, finished.DoneAt)
}

// TestUpdateTaskFieldsLeavesOtherColumnsAlone pins the property the workflow
// driver depends on: a caller holding a stale copy of a task (an HTTP handler
// that loaded it before the engine wrote) cannot undo the engine's write by
// updating an unrelated field.
func TestUpdateTaskFieldsLeavesOtherColumnsAlone(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := Company{Name: "HeadCount1", ShortName: "hc1"}
	require.NoError(t, database.Create(&company).Error)
	q := New(database)
	ctx := context.Background()
	stale, err := q.CreateTask(ctx, Task{CompanyID: company.ID, Title: "before", Status: TaskStatusTodo})
	require.NoError(t, err)

	// The engine moves the task on after the stale copy was read.
	require.NoError(t, database.Model(&Task{}).Where("id = ?", stale.ID).
		Updates(map[string]interface{}{"status": TaskStatusInProgress, "run_id": 77}).Error)

	updated, err := q.UpdateTaskFields(ctx, stale.ID, map[string]interface{}{"title": "after"})
	require.NoError(t, err)
	require.Equal(t, "after", updated.Title)
	require.Equal(t, TaskStatusInProgress, updated.Status, "the engine's status must survive an unrelated edit")
	require.NotNil(t, updated.RunID)
	require.Equal(t, int32(77), *updated.RunID, "the engine's run must survive an unrelated edit")
}

func TestUpdateTaskFieldsTracksDoneAt(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := Company{Name: "HeadCount1", ShortName: "hc1"}
	require.NoError(t, database.Create(&company).Error)
	q := New(database)
	ctx := context.Background()
	task, err := q.CreateTask(ctx, Task{CompanyID: company.ID, Title: "t", Status: TaskStatusInProgress})
	require.NoError(t, err)

	done, err := q.UpdateTaskFields(ctx, task.ID, map[string]interface{}{"status": TaskStatusDone})
	require.NoError(t, err)
	require.NotNil(t, done.DoneAt)
	require.WithinDuration(t, time.Now(), *done.DoneAt, 2*time.Second)
	doneAt := *done.DoneAt

	// Re-asserting done, or editing another field, keeps the original time.
	again, err := q.UpdateTaskFields(ctx, task.ID, map[string]interface{}{"status": TaskStatusDone, "title": "renamed"})
	require.NoError(t, err)
	require.NotNil(t, again.DoneAt)
	require.True(t, doneAt.Equal(*again.DoneAt))
	renamed, err := q.UpdateTaskFields(ctx, task.ID, map[string]interface{}{"priority": "High"})
	require.NoError(t, err)
	require.NotNil(t, renamed.DoneAt)

	reopened, err := q.UpdateTaskFields(ctx, task.ID, map[string]interface{}{"status": TaskStatusInProgress})
	require.NoError(t, err)
	require.Nil(t, reopened.DoneAt)

	// No fields is a read, not an error.
	same, err := q.UpdateTaskFields(ctx, task.ID, nil)
	require.NoError(t, err)
	require.Equal(t, TaskStatusInProgress, same.Status)
}

// Subtasks get "-1", "-2" suffixes on their parent's key; a deleted sibling's
// index is never reused, and a nested subtask builds on its parent's key.
func TestSubtaskRefKeysAreNeverReused(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := Company{Name: "HeadCount1", ShortName: "hc1"}
	require.NoError(t, database.Create(&company).Error)
	q := New(database)
	ctx := context.Background()
	root, err := q.CreateTask(ctx, Task{CompanyID: company.ID, Title: "root"})
	require.NoError(t, err)
	require.Regexp(t, `^HC1-\d+$`, root.RefKey)
	child := func(parent Task, title string) Task {
		task, err := q.CreateTask(ctx, Task{CompanyID: company.ID, ParentID: &parent.ID, Title: title})
		require.NoError(t, err)
		return task
	}
	first, second := child(root, "first"), child(root, "second")
	require.Equal(t, root.RefKey+"-1", first.RefKey)
	require.Equal(t, root.RefKey+"-2", second.RefKey)

	require.NoError(t, database.Delete(&Task{}, first.ID).Error)
	require.Equal(t, root.RefKey+"-3", child(root, "third").RefKey, "a deleted sibling's index is not reused")
	nested := child(second, "nested")
	require.Equal(t, second.RefKey+"-1", nested.RefKey)

	top, err := q.GetRootTask(ctx, nested.ID)
	require.NoError(t, err)
	require.Equal(t, root.ID, top.ID)
}
