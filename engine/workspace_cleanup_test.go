package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/eventhub"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWorkspaceCleanupEligibilityRequiresDoneRetention(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-doneWorkspaceRetention)
	recent := now.Add(-doneWorkspaceRetention + time.Minute)
	future := now.Add(time.Minute)
	for name, task := range map[string]db.Task{
		"old done":          {Status: db.TaskStatusDone, DoneAt: &old},
		"recent done":       {Status: db.TaskStatusDone, DoneAt: &recent},
		"active":            {Status: db.TaskStatusInProgress, DoneAt: &old},
		"missing timestamp": {Status: db.TaskStatusDone},
		"future timestamp":  {Status: db.TaskStatusDone, DoneAt: &future},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, name == "old done", workspaceCleanupEligible(task, now, doneWorkspaceRetention))
		})
	}
}

func TestCleanupExpiredDoneWorkspacesRemovesOnlyExpiredTaskData(t *testing.T) {
	basePath := t.TempDir()
	t.Setenv("E2E_HEADCOUNT1_HOME", basePath)
	database, err := gorm.Open(sqlite.Open(filepath.Join(basePath, "test.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := db.Company{Name: "Acme", ShortName: "ACME"}
	require.NoError(t, database.Create(&company).Error)
	old := time.Now().Add(-doneWorkspaceRetention - time.Hour)
	recent := time.Now().Add(-doneWorkspaceRetention + time.Hour)
	oldTask := db.Task{CompanyID: company.ID, Title: "old", Status: db.TaskStatusDone, DoneAt: &old}
	recentTask := db.Task{CompanyID: company.ID, Title: "recent", Status: db.TaskStatusDone, DoneAt: &recent}
	activeTask := db.Task{CompanyID: company.ID, Title: "active", Status: db.TaskStatusInProgress}
	for _, task := range []*db.Task{&oldTask, &recentTask, &activeTask} {
		require.NoError(t, database.Create(task).Error)
	}
	paths := dbHeadcountPaths(basePath, company.ShortName)
	for _, task := range []db.Task{oldTask, recentTask, activeTask} {
		require.NoError(t, os.MkdirAll(filepath.Join(paths, "task-"+itoa(task.ID)), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(paths, "session-task-"+itoa(task.ID)+"-run-99"), 0o755))
	}

	eng := NewNativeEngine(database, eventhub.NewHub())
	cleaned, err := eng.CleanupExpiredDoneWorkspaces(context.Background(), time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, cleaned)
	require.NoDirExists(t, filepath.Join(paths, "task-"+itoa(oldTask.ID)))
	require.NoDirExists(t, filepath.Join(paths, "session-task-"+itoa(oldTask.ID)+"-run-99"))
	require.DirExists(t, filepath.Join(paths, "task-"+itoa(recentTask.ID)))
	require.DirExists(t, filepath.Join(paths, "session-task-"+itoa(recentTask.ID)+"-run-99"))
	require.DirExists(t, filepath.Join(paths, "task-"+itoa(activeTask.ID)))
}

func dbHeadcountPaths(basePath, company string) string {
	return filepath.Join(basePath, ".headcount1", "workspace", company)
}

func itoa(id int32) string {
	return fmt.Sprintf("%d", id)
}
