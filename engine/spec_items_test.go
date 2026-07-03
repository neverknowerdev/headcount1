package engine_test

import (
	"context"
	"testing"

	"agent-orchestrator/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpecItemsLifecycle covers the definition → independence → verdict flow.
func TestSpecItemsLifecycle(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	task := seedTestData(t, database, "http://unused")
	definerRun, err := q.CreateRun(ctx, db.Run{TaskID: task.ID, AgentID: *task.AgentID, Status: "completed"})
	require.NoError(t, err)
	verifierRun, err := q.CreateRun(ctx, db.Run{TaskID: task.ID, AgentID: *task.AgentID, Status: "running"})
	require.NoError(t, err)

	items, err := q.ReplaceSpecItems(ctx, task.ID, "criterion", []string{"has a summary", "cites sources"}, definerRun.ID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "pending", items[0].Status)

	pending, err := q.CountPendingSpecItems(ctx, task.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, pending)

	// Independence: the defining run may not verify its own items.
	err = q.UpdateSpecItemVerdict(ctx, items[0].ID, "passed", "self-graded", definerRun.ID)
	require.Error(t, err)

	// A different run can.
	require.NoError(t, q.UpdateSpecItemVerdict(ctx, items[0].ID, "passed", "summary is in §1", verifierRun.ID))
	require.NoError(t, q.UpdateSpecItemVerdict(ctx, items[1].ID, "failed", "no sources cited", verifierRun.ID))

	pending, err = q.CountPendingSpecItems(ctx, task.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, pending)

	// Redefining replaces only pending items — verified verdicts survive.
	items2, err := q.ReplaceSpecItems(ctx, task.ID, "criterion", []string{"new pending item"}, definerRun.ID)
	require.NoError(t, err)
	require.Len(t, items2, 1)
	all, err := q.ListSpecItemsByTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Len(t, all, 3, "2 verified survivors + 1 new pending")
}

// TestGetRootTask walks a two-level subtask chain to the root.
func TestGetRootTask(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	root := seedTestData(t, database, "http://unused")
	rootID := root.ID
	child, err := q.CreateTask(ctx, db.Task{CompanyID: root.CompanyID, SprintID: root.SprintID, ParentID: &rootID, Title: "child", TaskType: db.TaskTypeImplement, Status: "to-do", Priority: "Normal"})
	require.NoError(t, err)
	childID := child.ID
	grandchild, err := q.CreateTask(ctx, db.Task{CompanyID: root.CompanyID, SprintID: root.SprintID, ParentID: &childID, Title: "grandchild", TaskType: db.TaskTypeImplement, Status: "to-do", Priority: "Normal"})
	require.NoError(t, err)

	got, err := q.GetRootTask(ctx, grandchild.ID)
	require.NoError(t, err)
	assert.Equal(t, root.ID, got.ID)

	got, err = q.GetRootTask(ctx, root.ID)
	require.NoError(t, err)
	assert.Equal(t, root.ID, got.ID)
}

// TestArtifactOverwriteTracking: same filename on the same task updates the
// existing row instead of accumulating duplicates.
func TestArtifactOverwriteTracking(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	task := seedTestData(t, database, "http://unused")
	run1, err := q.CreateRun(ctx, db.Run{TaskID: task.ID, AgentID: *task.AgentID, Status: "completed"})
	require.NoError(t, err)
	run2, err := q.CreateRun(ctx, db.Run{TaskID: task.ID, AgentID: *task.AgentID, Status: "running"})
	require.NoError(t, err)

	a, err := q.CreateArtifact(ctx, db.Artifact{TaskID: task.ID, RunID: run1.ID, Filename: "report.md", FilePath: "/tmp/report.md", Content: "v1"})
	require.NoError(t, err)

	found, err := q.GetArtifactByTaskAndFilename(ctx, task.ID, "report.md")
	require.NoError(t, err)
	assert.Equal(t, a.ID, found.ID)
	assert.Equal(t, run1.ID, found.RunID)

	require.NoError(t, q.UpdateArtifactContent(ctx, a.ID, "v2", run2.ID))
	found, err = q.GetArtifactByTaskAndFilename(ctx, task.ID, "report.md")
	require.NoError(t, err)
	assert.Equal(t, "v2", found.Content)
	assert.Equal(t, run2.ID, found.RunID)
}
