package engine_test

import (
	"context"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetRootTask walks a two-level subtask chain to the root.
func TestGetRootTask(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	root := seedTestData(t, database, "http://unused")
	rootID := root.ID
	child, err := q.CreateTask(ctx, db.Task{CompanyID: root.CompanyID, SprintID: root.SprintID, ParentID: &rootID, Title: "child", Status: "to-do", Priority: "Normal"})
	require.NoError(t, err)
	childID := child.ID
	grandchild, err := q.CreateTask(ctx, db.Task{CompanyID: root.CompanyID, SprintID: root.SprintID, ParentID: &childID, Title: "grandchild", Status: "to-do", Priority: "Normal"})
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

// TestTaskRefKeys: main tasks get COMPANY-ID keys, subtasks get -1, -2
// suffixes, and deleted siblings never cause key reuse.
func TestTaskRefKeys(t *testing.T) {
	database := setupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	root := seedTestData(t, database, "http://unused")
	require.NotEmpty(t, root.RefKey)
	assert.Regexp(t, `^[A-Z]+-\d+$`, root.RefKey)

	rootID := root.ID
	mk := func(title string) db.Task {
		sub, err := q.CreateTask(ctx, db.Task{CompanyID: root.CompanyID, SprintID: root.SprintID, ParentID: &rootID, Title: title, Status: "to-do", Priority: "Normal"})
		require.NoError(t, err)
		return sub
	}
	s1 := mk("first")
	s2 := mk("second")
	assert.Equal(t, root.RefKey+"-1", s1.RefKey)
	assert.Equal(t, root.RefKey+"-2", s2.RefKey)

	// Delete the first subtask; the next one must NOT reuse index 1.
	require.NoError(t, database.Delete(&db.Task{}, s1.ID).Error)
	s3 := mk("third")
	assert.Equal(t, root.RefKey+"-3", s3.RefKey)

	// Nested subtask builds on the parent's key.
	s2ID := s2.ID
	nested, err := q.CreateTask(ctx, db.Task{CompanyID: root.CompanyID, SprintID: root.SprintID, ParentID: &s2ID, Title: "nested", Status: "to-do", Priority: "Normal"})
	require.NoError(t, err)
	assert.Equal(t, s2.RefKey+"-1", nested.RefKey)
}

// TestDeriveShortName covers the agent short-name fallback.
func TestDeriveShortName(t *testing.T) {
	cases := map[string]string{
		"Programmer":         "PROGRAM",
		"QA":                 "QA",
		"QA Lead":            "QL",
		"TechSpecResearcher": "TECHSPE",
		"":                   "AGENT",
	}
	for in, want := range cases {
		assert.Equal(t, want, agentconfig.DeriveShortName(in), "input %q", in)
	}
}
