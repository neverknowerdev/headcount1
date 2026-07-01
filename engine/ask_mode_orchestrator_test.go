package engine

import (
	"context"
	"testing"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupRoleModelTestDB(t *testing.T) *db.Queries {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.LLMProvider{}))
	return db.New(database)
}

func setupExecuteTaskTestDB(t *testing.T) *db.Queries {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.LLMProvider{}, &db.Task{}, &db.Session{}, &db.PendingQuestion{}))
	return db.New(database)
}

// TestValidateRoleModels_NoProvidersFails verifies that with zero LLM
// providers configured, validation fails immediately and names every role
// that couldn't be resolved — this is the only situation in which a role
// should ever have no usable model.
func TestValidateRoleModels_NoProvidersFails(t *testing.T) {
	q := setupRoleModelTestDB(t)
	orch := newAskModeOrchestrator(q, nil, nil, Settings{})

	err := orch.validateRoleModels(context.Background(), db.Task{TaskType: db.TaskTypeTech})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SmartPlanner")
	assert.Contains(t, err.Error(), "TechSpecResearcher")
	assert.Contains(t, err.Error(), "Coder")
	assert.Contains(t, err.Error(), "Tester")
}

// TestValidateRoleModels_WithProviderSucceeds verifies that once at least one
// provider exists, all roles resolve via the "first available provider"
// fallback even without explicit Role Model Configuration.
func TestValidateRoleModels_WithProviderSucceeds(t *testing.T) {
	q := setupRoleModelTestDB(t)
	_, err := q.CreateLLMProvider(context.Background(), db.LLMProvider{
		Name: "Only Provider", BaseUrl: "http://example.test", DefaultModel: "model-a",
	})
	require.NoError(t, err)

	orch := newAskModeOrchestrator(q, nil, nil, Settings{})
	err = orch.validateRoleModels(context.Background(), db.Task{TaskType: db.TaskTypeTech})

	assert.NoError(t, err)
}

// TestValidateRoleModels_DanglingProviderIDFails verifies that a role
// explicitly configured to a provider ID that no longer exists (e.g. it was
// deleted) fails validation rather than silently falling back, so a stale
// config surfaces as a clear error instead of a runtime crash mid-pipeline.
func TestValidateRoleModels_DanglingProviderIDFails(t *testing.T) {
	q := setupRoleModelTestDB(t)
	_, err := q.CreateLLMProvider(context.Background(), db.LLMProvider{
		Name: "Provider", BaseUrl: "http://example.test", DefaultModel: "model-a",
	})
	require.NoError(t, err)

	settings := Settings{RoleModels: RoleModelConfig{CoderProviderID: 9999}}
	orch := newAskModeOrchestrator(q, nil, nil, settings)

	err = orch.validateRoleModels(context.Background(), db.Task{TaskType: db.TaskTypeTech})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Coder")
}

// TestExecuteTask_FailsFastWithNoProviders verifies ExecuteTask returns the
// role-model validation error immediately, before creating any session or
// running the refinement stage.
func TestExecuteTask_FailsFastWithNoProviders(t *testing.T) {
	q := setupExecuteTaskTestDB(t)

	orch := newAskModeOrchestrator(q, nil, nil, Settings{})
	task := db.Task{ID: 1, TaskType: db.TaskTypeTech, Title: "Test task"}

	finished, err := orch.ExecuteTask(context.Background(), task, db.Run{}, t.TempDir(), nil)

	assert.False(t, finished)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no model configured")

	sessions, listErr := q.GetTaskSessions(context.Background(), task.ID)
	require.NoError(t, listErr)
	assert.Empty(t, sessions, "no orchestrator session should be created when role model validation fails")
}
