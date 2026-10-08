package db

import (
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/db/models"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// workflowFixture is a migrated database with one company, sprint and root
// task.
type workflowFixture struct {
	database *gorm.DB
	q        *Queries
	company  Company
	sprint   Sprint
	root     Task
}

// newSQLiteWorkflowFixture opens SQLite the way production does: one
// connection, foreign keys enforced.
func newSQLiteWorkflowFixture(t *testing.T) workflowFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.db")
	database, err := gorm.Open(sqlite.Open(path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	return seedWorkflowFixture(t, database)
}

// newPostgresWorkflowFixture gives each test a schema of its own on a real
// PostgreSQL server, with a connection pool, so the same assertions also face
// PostgreSQL's SQL dialect and genuinely concurrent connections.
func newPostgresWorkflowFixture(t *testing.T) workflowFixture {
	t.Helper()
	url := os.Getenv("TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping Postgres workflow test")
	}
	schema := fmt.Sprintf("wf_test_%d", time.Now().UnixNano())
	dsn, err := migrations.PostgresSearchPath(url, schema)
	require.NoError(t, err)
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	require.NoError(t, database.Exec(`CREATE SCHEMA "`+schema+`"`).Error)
	t.Cleanup(func() {
		_ = database.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error
		_ = sqlDB.Close()
	})
	require.NoError(t, migrations.ApplyWithSchema(context.Background(), sqlDB, "postgres", "test", schema))
	return seedWorkflowFixture(t, database)
}

func seedWorkflowFixture(t *testing.T, database *gorm.DB) workflowFixture {
	t.Helper()
	f := workflowFixture{database: database, q: New(database)}
	f.company = Company{Name: "HeadCount1", ShortName: "hc1"}
	require.NoError(t, database.Create(&f.company).Error)
	f.sprint = Sprint{CompanyID: f.company.ID, Name: "Sprint 1"}
	require.NoError(t, database.Create(&f.sprint).Error)
	var err error
	f.root, err = f.q.CreateTask(context.Background(), Task{
		CompanyID: f.company.ID, SprintID: f.sprint.ID, Title: "root", Status: TaskStatusInProgress,
		TaskType: models.TaskTypeCoding, Phase: models.TaskPhasePlan,
	})
	require.NoError(t, err)
	return f
}

// workflowRepositoryTests are the workflow repository's behaviours. Each runs
// against SQLite always and against PostgreSQL when a server is configured.
var workflowRepositoryTests = []struct {
	name string
	run  func(*testing.T, workflowFixture)
}{
	{"CreateTaskRecordsTreePosition", workflowCreateTaskRecordsTreePosition},
	{"TaskLeaseIsExclusiveUntilItExpires", workflowTaskLeaseIsExclusiveUntilItExpires},
	{"TaskLeaseHasOneWinnerUnderContention", workflowTaskLeaseHasOneWinnerUnderContention},
	{"WorkflowTransitionCommitsAStepAsOneUnit", workflowWorkflowTransitionCommitsAStepAsOneUnit},
	{"RedirectDependentsMovesWaitingTasksToANewPrerequisite", workflowRedirectDependentsMovesWaitingTasksToANewPrerequisite},
	{"WorkflowTransitionWritesNothingWhenItFails", workflowWorkflowTransitionWritesNothingWhenItFails},
	{"WorkflowTransitionRejectsAStaleView", workflowWorkflowTransitionRejectsAStaleView},
	{"WorkflowTransitionCanReleaseTheLeaseWithTheNewState", workflowWorkflowTransitionCanReleaseTheLeaseWithTheNewState},
	{"WorkspaceHasOneWriterPerTree", workflowWorkspaceHasOneWriterPerTree},
	{"CancelDescendantsStopsEverythingBeneathATask", workflowCancelDescendantsStopsEverythingBeneathATask},
	{"ListTasksToAdvanceFindsStartedTasksNobodyHolds", workflowListTasksToAdvanceFindsStartedTasksNobodyHolds},
}

func TestWorkflowRepository(t *testing.T) {
	for _, test := range workflowRepositoryTests {
		t.Run(test.name, func(t *testing.T) { test.run(t, newSQLiteWorkflowFixture(t)) })
	}
}

// TestPostgresWorkflowRepository is named for the Postgres CI job, which runs
// tests matching ^TestPostgres.
func TestPostgresWorkflowRepository(t *testing.T) {
	for _, test := range workflowRepositoryTests {
		t.Run(test.name, func(t *testing.T) { test.run(t, newPostgresWorkflowFixture(t)) })
	}
}

func (f workflowFixture) reload(t *testing.T, id int32) Task {
	t.Helper()
	task, err := f.q.GetTask(context.Background(), id)
	require.NoError(t, err)
	return task
}

func workflowCreateTaskRecordsTreePosition(t *testing.T, f workflowFixture) {
	ctx := context.Background()
	require.Equal(t, f.root.ID, f.root.RootTaskID)
	require.Zero(t, f.root.Depth)

	child, err := f.q.CreateTask(ctx, Task{CompanyID: f.company.ID, SprintID: f.sprint.ID, ParentID: &f.root.ID, Title: "child"})
	require.NoError(t, err)
	grandchild, err := f.q.CreateTask(ctx, Task{CompanyID: f.company.ID, SprintID: f.sprint.ID, ParentID: &child.ID, Title: "grandchild"})
	require.NoError(t, err)
	for _, task := range []Task{f.reload(t, child.ID), f.reload(t, grandchild.ID)} {
		require.Equal(t, f.root.ID, task.RootTaskID, task.Title)
	}
	require.Equal(t, 1, f.reload(t, child.ID).Depth)
	require.Equal(t, 2, f.reload(t, grandchild.ID).Depth)
	require.Equal(t, models.TaskTypeGeneral, f.reload(t, child.ID).TaskType, "the type defaults to general")
	require.Equal(t, models.TaskModeManaged, f.reload(t, child.ID).Mode)

	// Moving a task to another parent moves everything beneath it too.
	other, err := f.q.CreateTask(ctx, Task{CompanyID: f.company.ID, SprintID: f.sprint.ID, Title: "other root"})
	require.NoError(t, err)
	_, err = f.q.UpdateTaskFields(ctx, child.ID, map[string]interface{}{"parent_id": other.ID})
	require.NoError(t, err)
	require.Equal(t, other.ID, f.reload(t, child.ID).RootTaskID)
	require.Equal(t, other.ID, f.reload(t, grandchild.ID).RootTaskID)
	require.Equal(t, 2, f.reload(t, grandchild.ID).Depth)
	require.Equal(t, f.root.ID, f.reload(t, f.root.ID).RootTaskID, "an unrelated tree is untouched")
}

func workflowTaskLeaseIsExclusiveUntilItExpires(t *testing.T, f workflowFixture) {
	ctx := context.Background()

	got, err := f.q.AcquireTaskLease(ctx, f.root.ID, "driver-a", time.Minute)
	require.NoError(t, err)
	require.True(t, got)
	got, err = f.q.AcquireTaskLease(ctx, f.root.ID, "driver-b", time.Minute)
	require.NoError(t, err)
	require.False(t, got, "a held lease cannot be taken")

	renewed, err := f.q.RenewTaskLease(ctx, f.root.ID, "driver-b", time.Minute)
	require.NoError(t, err)
	require.False(t, renewed, "only the holder can renew")
	renewed, err = f.q.RenewTaskLease(ctx, f.root.ID, "driver-a", time.Minute)
	require.NoError(t, err)
	require.True(t, renewed)

	// Releasing as a non-holder changes nothing.
	require.NoError(t, f.q.ReleaseTaskLease(ctx, f.root.ID, "driver-b"))
	got, err = f.q.AcquireTaskLease(ctx, f.root.ID, "driver-b", time.Minute)
	require.NoError(t, err)
	require.False(t, got)

	require.NoError(t, f.q.ReleaseTaskLease(ctx, f.root.ID, "driver-a"))
	got, err = f.q.AcquireTaskLease(ctx, f.root.ID, "driver-b", time.Minute)
	require.NoError(t, err)
	require.True(t, got, "a released lease is free")

	// A crashed holder's lease expires and is taken over; the old holder can
	// then neither renew nor write.
	require.NoError(t, f.database.Model(&Task{}).Where("id = ?", f.root.ID).
		UpdateColumn("lease_until", time.Now().Add(-time.Second)).Error)
	got, err = f.q.AcquireTaskLease(ctx, f.root.ID, "driver-c", time.Minute)
	require.NoError(t, err)
	require.True(t, got, "an expired lease is free")
	renewed, err = f.q.RenewTaskLease(ctx, f.root.ID, "driver-b", time.Minute)
	require.NoError(t, err)
	require.False(t, renewed)
	err = f.q.WorkflowTransition(ctx, GuardFor(f.reload(t, f.root.ID), "driver-b"), func(tx *WorkflowTx) error {
		return tx.UpdateTask(map[string]interface{}{"phase": models.TaskPhaseExecute})
	})
	require.ErrorIs(t, err, ErrLeaseLost)
	require.Equal(t, models.TaskPhasePlan, f.reload(t, f.root.ID).Phase)
}

func workflowTaskLeaseHasOneWinnerUnderContention(t *testing.T, f workflowFixture) {
	const drivers = 16
	var wg sync.WaitGroup
	wins := make(chan string, drivers)
	for i := 0; i < drivers; i++ {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			got, err := f.q.AcquireTaskLease(context.Background(), f.root.ID, owner, time.Minute)
			if err == nil && got {
				wins <- owner
			}
		}(string(rune('a' + i)))
	}
	wg.Wait()
	close(wins)
	var winners []string
	for owner := range wins {
		winners = append(winners, owner)
	}
	require.Len(t, winners, 1)
	require.Equal(t, winners[0], f.reload(t, f.root.ID).LeaseOwner)
}

func workflowWorkflowTransitionCommitsAStepAsOneUnit(t *testing.T, f workflowFixture) {
	ctx := context.Background()
	held, err := f.q.AcquireTaskLease(ctx, f.root.ID, "driver", time.Minute)
	require.NoError(t, err)
	require.True(t, held)
	root := f.reload(t, f.root.ID)

	var step TaskStep
	var first, second, third Task
	err = f.q.WorkflowTransition(ctx, GuardFor(root, "driver"), func(tx *WorkflowTx) error {
		var txErr error
		step, txErr = tx.AppendStep(TaskStep{Kind: models.StepSmartCall, Phase: root.Phase, ToolName: "create_tasks", Prompt: "p", Response: "r"})
		if txErr != nil {
			return txErr
		}
		if _, txErr = tx.AddDecision(Decision{StepID: &step.ID, Phase: root.Phase, Title: "Split by layer", Rationale: "independent review"}); txErr != nil {
			return txErr
		}
		newChild := func(title string) Task {
			return Task{Title: title, Status: TaskStatusTodo, Mode: models.TaskModeDirect, TaskType: models.TaskTypeCoding,
				OriginStepID: &step.ID, OriginPhase: root.Phase, WorkflowPhase: models.TaskPhaseExecute}
		}
		if first, txErr = tx.CreateSubtask(newChild("schema"), nil); txErr != nil {
			return txErr
		}
		if second, txErr = tx.CreateSubtask(newChild("api"), []int32{first.ID}); txErr != nil {
			return txErr
		}
		if third, txErr = tx.CreateSubtask(newChild("docs"), nil); txErr != nil {
			return txErr
		}
		return tx.UpdateTask(map[string]interface{}{
			"phase": models.TaskPhaseExecute, "waiting_on": models.TaskWaitSubtasks,
			"smart_steps_used": gorm.Expr("smart_steps_used + 1"),
		})
	})
	require.NoError(t, err)

	after := f.reload(t, f.root.ID)
	require.Equal(t, models.TaskPhaseExecute, after.Phase)
	require.Equal(t, models.TaskWaitSubtasks, after.WaitingOn)
	require.Equal(t, 1, after.SmartStepsUsed)
	require.Equal(t, "driver", after.LeaseOwner, "the lease is kept unless the step releases it")

	var children []Task
	require.NoError(t, f.database.Where("parent_id = ?", f.root.ID).Order("id").Find(&children).Error)
	require.Len(t, children, 3)
	for i, child := range children {
		require.Equal(t, f.root.ID, child.RootTaskID)
		require.Equal(t, 1, child.Depth)
		require.Equal(t, f.company.ID, child.CompanyID)
		require.Equal(t, f.sprint.ID, child.SprintID)
		require.Equal(t, root.RefKey+"-"+string(rune('1'+i)), child.RefKey, "children get consecutive ref keys")
		require.Equal(t, root.GitHubBranch, child.GitHubBranch, "the tree shares one branch")
		require.NotNil(t, child.OriginStepID)
		require.Equal(t, step.ID, *child.OriginStepID)
		require.Equal(t, models.TaskModeDirect, child.Mode)
	}

	// The chain edge gates the dependent; the independent sibling is free.
	ready, blockers, err := f.q.CanStartTask(ctx, second.ID)
	require.NoError(t, err)
	require.False(t, ready)
	require.Len(t, blockers, 1)
	require.Equal(t, first.ID, blockers[0].ID)
	ready, _, err = f.q.CanStartTask(ctx, third.ID)
	require.NoError(t, err)
	require.True(t, ready)

	steps, err := f.q.ListTaskStepHeads(ctx, f.root.ID)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	require.Equal(t, f.root.ID, steps[0].RootTaskID)
	require.False(t, steps[0].CreatedAt.IsZero())
	decisions, err := f.q.ListDecisionsByRoot(ctx, f.root.ID)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, models.DecisionKindDecision, decisions[0].Kind)
	require.Equal(t, step.ID, *decisions[0].StepID)
}

// When a review asks for changes, what waited for it waits for the review of
// the fix instead; the fix itself goes on following the review it answers.
func workflowRedirectDependentsMovesWaitingTasksToANewPrerequisite(t *testing.T, f workflowFixture) {
	ctx := context.Background()
	_, err := f.q.AcquireTaskLease(ctx, f.root.ID, "driver", time.Minute)
	require.NoError(t, err)
	root := f.reload(t, f.root.ID)
	child := func(title string) Task {
		return Task{Title: title, Status: TaskStatusTodo, Mode: models.TaskModeDirect, TaskType: models.TaskTypeCoding}
	}

	var schema, review, api, fix, secondReview Task
	err = f.q.WorkflowTransition(ctx, GuardFor(root, "driver"), func(tx *WorkflowTx) error {
		var txErr error
		if schema, txErr = tx.CreateSubtask(child("schema"), nil); txErr != nil {
			return txErr
		}
		if review, txErr = tx.CreateSubtask(child("review"), []int32{schema.ID}); txErr != nil {
			return txErr
		}
		api, txErr = tx.CreateSubtask(child("api"), []int32{schema.ID, review.ID})
		return txErr
	})
	require.NoError(t, err)

	err = f.q.WorkflowTransition(ctx, GuardFor(f.reload(t, f.root.ID), "driver"), func(tx *WorkflowTx) error {
		var txErr error
		if fix, txErr = tx.CreateSubtask(child("fix"), []int32{review.ID}); txErr != nil {
			return txErr
		}
		if secondReview, txErr = tx.CreateSubtask(child("second review"), []int32{fix.ID}); txErr != nil {
			return txErr
		}
		return tx.RedirectDependents(review.ID, secondReview.ID, []int32{fix.ID, secondReview.ID})
	})
	require.NoError(t, err)

	prerequisitesOf := func(taskID int32) []int32 {
		tasks, err := f.q.ListPrerequisites(ctx, taskID)
		require.NoError(t, err)
		ids := make([]int32, 0, len(tasks))
		for _, task := range tasks {
			ids = append(ids, task.ID)
		}
		return ids
	}
	require.ElementsMatch(t, []int32{schema.ID, secondReview.ID}, prerequisitesOf(api.ID))
	require.Equal(t, []int32{review.ID}, prerequisitesOf(fix.ID))
	require.Equal(t, []int32{schema.ID}, prerequisitesOf(review.ID))
	require.Equal(t, []int32{fix.ID}, prerequisitesOf(secondReview.ID))
}

func workflowWorkflowTransitionWritesNothingWhenItFails(t *testing.T, f workflowFixture) {
	ctx := context.Background()
	_, err := f.q.AcquireTaskLease(ctx, f.root.ID, "driver", time.Minute)
	require.NoError(t, err)
	root := f.reload(t, f.root.ID)
	boom := errors.New("boom")

	// A step that fails after creating a subtask, a journal entry and a state
	// change leaves no trace of any of them.
	err = f.q.WorkflowTransition(ctx, GuardFor(root, "driver"), func(tx *WorkflowTx) error {
		step, txErr := tx.AppendStep(TaskStep{Kind: models.StepSmartCall})
		require.NoError(t, txErr)
		_, txErr = tx.CreateSubtask(Task{Title: "orphan", OriginStepID: &step.ID}, nil)
		require.NoError(t, txErr)
		require.NoError(t, tx.UpdateTask(map[string]interface{}{"phase": models.TaskPhaseExecute}))
		return boom
	})
	require.ErrorIs(t, err, boom)

	// So does one whose last write is rejected by the database.
	err = f.q.WorkflowTransition(ctx, GuardFor(root, "driver"), func(tx *WorkflowTx) error {
		if _, txErr := tx.CreateSubtask(Task{Title: "also orphan"}, nil); txErr != nil {
			return txErr
		}
		return tx.UpdateTask(map[string]interface{}{"waiting_on": "not-a-wait"})
	})
	require.Error(t, err)

	var children []Task
	require.NoError(t, f.database.Where("parent_id = ?", f.root.ID).Find(&children).Error)
	require.Empty(t, children)
	steps, err := f.q.ListTaskStepHeads(ctx, f.root.ID)
	require.NoError(t, err)
	require.Empty(t, steps)
	require.Equal(t, models.TaskPhasePlan, f.reload(t, f.root.ID).Phase)
}

func workflowWorkflowTransitionRejectsAStaleView(t *testing.T, f workflowFixture) {
	ctx := context.Background()
	_, err := f.q.AcquireTaskLease(ctx, f.root.ID, "driver", time.Minute)
	require.NoError(t, err)
	stale := f.reload(t, f.root.ID)

	// The task changes after the driver read it (a stop, say).
	require.NoError(t, f.database.Model(&Task{}).Where("id = ?", f.root.ID).
		UpdateColumn("status", TaskStatusBlocked).Error)

	ran := false
	err = f.q.WorkflowTransition(ctx, GuardFor(stale, "driver"), func(tx *WorkflowTx) error {
		ran = true
		return nil
	})
	require.ErrorIs(t, err, ErrTransitionConflict)
	require.False(t, ran)

	err = f.q.WorkflowTransition(ctx, TransitionGuard{TaskID: f.root.ID}, func(tx *WorkflowTx) error { return nil })
	require.Error(t, err, "a transition without a lease owner is refused")
}

func workflowWorkflowTransitionCanReleaseTheLeaseWithTheNewState(t *testing.T, f workflowFixture) {
	ctx := context.Background()
	_, err := f.q.AcquireTaskLease(ctx, f.root.ID, "driver", time.Minute)
	require.NoError(t, err)
	err = f.q.WorkflowTransition(ctx, GuardFor(f.reload(t, f.root.ID), "driver"), func(tx *WorkflowTx) error {
		if txErr := tx.UpdateTask(map[string]interface{}{"status": TaskStatusDone, "phase": ""}); txErr != nil {
			return txErr
		}
		return tx.ReleaseLease()
	})
	require.NoError(t, err)
	done := f.reload(t, f.root.ID)
	require.Equal(t, TaskStatusDone, done.Status)
	require.NotNil(t, done.DoneAt)
	require.Empty(t, done.LeaseOwner)
	require.Nil(t, done.LeaseUntil)
}

func workflowWorkspaceHasOneWriterPerTree(t *testing.T, f workflowFixture) {
	ctx := context.Background()
	newWriter := func(title string) Task {
		task, err := f.q.CreateTask(ctx, Task{CompanyID: f.company.ID, SprintID: f.sprint.ID, ParentID: &f.root.ID,
			Title: title, Status: TaskStatusInProgress, Mode: models.TaskModeDirect})
		require.NoError(t, err)
		_, err = f.q.AcquireTaskLease(ctx, task.ID, "driver", time.Minute)
		require.NoError(t, err)
		return f.reload(t, task.ID)
	}
	claim := func(task Task) bool {
		var claimed bool
		require.NoError(t, f.q.WorkflowTransition(ctx, GuardFor(task, "driver"), func(tx *WorkflowTx) error {
			var txErr error
			claimed, txErr = tx.ClaimWorkspace()
			return txErr
		}))
		return claimed
	}
	first, second := newWriter("first"), newWriter("second")

	require.True(t, claim(first))
	require.True(t, claim(first), "claiming again as the holder succeeds")
	require.False(t, claim(second), "a second writer must wait")
	require.Equal(t, first.ID, *f.reload(t, f.root.ID).WorkspaceOwnerTaskID)

	// A release by a non-holder is a no-op; the holder's release frees it.
	require.NoError(t, f.q.WorkflowTransition(ctx, GuardFor(second, "driver"), func(tx *WorkflowTx) error { return tx.ReleaseWorkspace() }))
	require.False(t, claim(second))
	require.NoError(t, f.q.WorkflowTransition(ctx, GuardFor(first, "driver"), func(tx *WorkflowTx) error { return tx.ReleaseWorkspace() }))
	require.True(t, claim(second))
}

func workflowCancelDescendantsStopsEverythingBeneathATask(t *testing.T, f workflowFixture) {
	ctx := context.Background()
	create := func(parent int32, title, status string) Task {
		task, err := f.q.CreateTask(ctx, Task{CompanyID: f.company.ID, SprintID: f.sprint.ID, ParentID: &parent, Title: title, Status: status})
		require.NoError(t, err)
		return task
	}
	running := create(f.root.ID, "running", TaskStatusInProgress)
	nested := create(running.ID, "nested", TaskStatusTodo)
	finished := create(f.root.ID, "finished", TaskStatusDone)
	other, err := f.q.CreateTask(ctx, Task{CompanyID: f.company.ID, SprintID: f.sprint.ID, Title: "other tree", Status: TaskStatusInProgress})
	require.NoError(t, err)
	require.NoError(t, f.database.Model(&Task{}).Where("id = ?", f.root.ID).UpdateColumn("workspace_owner_task_id", running.ID).Error)
	require.NoError(t, f.database.Model(&Task{}).Where("id = ?", running.ID).
		UpdateColumns(map[string]interface{}{"waiting_on": models.TaskWaitRun, "run_id": 9}).Error)

	_, err = f.q.AcquireTaskLease(ctx, f.root.ID, "driver", time.Minute)
	require.NoError(t, err)
	var canceled []int32
	require.NoError(t, f.q.WorkflowTransition(ctx, GuardFor(f.reload(t, f.root.ID), "driver"), func(tx *WorkflowTx) error {
		var txErr error
		canceled, txErr = tx.CancelDescendants(models.TaskResultStopped)
		return txErr
	}))
	require.ElementsMatch(t, []int32{running.ID, nested.ID}, canceled)

	for _, id := range []int32{running.ID, nested.ID} {
		task := f.reload(t, id)
		require.Equal(t, TaskStatusCanceled, task.Status)
		require.Equal(t, models.TaskResultStopped, task.ResultReason)
		require.Empty(t, task.WaitingOn)
		require.Nil(t, task.RunID)
	}
	require.Equal(t, TaskStatusDone, f.reload(t, finished.ID).Status, "finished work is left as it is")
	require.Equal(t, TaskStatusInProgress, f.reload(t, other.ID).Status, "another tree is untouched")
	require.Equal(t, TaskStatusInProgress, f.reload(t, f.root.ID).Status, "the task itself is the caller's to change")
	require.Nil(t, f.reload(t, f.root.ID).WorkspaceOwnerTaskID, "a canceled writer gives up the worktree")

	// A canceled descendant's driver, mid-step, can no longer write.
	_, err = f.q.AcquireTaskLease(ctx, running.ID, "child-driver", time.Minute)
	require.NoError(t, err)
	stale := running
	stale.WaitingOn = models.TaskWaitRun
	err = f.q.WorkflowTransition(ctx, GuardFor(stale, "child-driver"), func(tx *WorkflowTx) error {
		return tx.UpdateTask(map[string]interface{}{"status": TaskStatusDone})
	})
	require.ErrorIs(t, err, ErrTransitionConflict)
}

func workflowListTasksToAdvanceFindsStartedTasksNobodyHolds(t *testing.T, f workflowFixture) {
	ctx := context.Background()
	create := func(title, status string) Task {
		task, err := f.q.CreateTask(ctx, Task{CompanyID: f.company.ID, SprintID: f.sprint.ID, Title: title, Status: status})
		require.NoError(t, err)
		return task
	}
	backlog := create("backlog", TaskStatusBacklog)
	queued := create("queued", TaskStatusTodo)
	blocked := create("blocked", TaskStatusBlocked)
	done := create("done", TaskStatusDone)
	held := create("held", TaskStatusInProgress)
	expired := create("expired", TaskStatusInProgress)
	archived := create("archived", TaskStatusInProgress)
	require.NoError(t, f.database.Model(&Task{}).Where("id = ?", archived.ID).UpdateColumn("is_archived", true).Error)
	_, err := f.q.AcquireTaskLease(ctx, held.ID, "live", time.Minute)
	require.NoError(t, err)
	_, err = f.q.AcquireTaskLease(ctx, expired.ID, "dead", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.database.Model(&Task{}).Where("id = ?", expired.ID).
		UpdateColumn("lease_until", time.Now().Add(-time.Second)).Error)

	ids, err := f.q.ListTasksToAdvance(ctx, 0)
	require.NoError(t, err)
	require.ElementsMatch(t, []int32{f.root.ID, queued.ID, blocked.ID, expired.ID}, ids)
	require.NotContains(t, ids, backlog.ID)
	require.NotContains(t, ids, done.ID)
	require.NotContains(t, ids, held.ID)

	limited, err := f.q.ListTasksToAdvance(ctx, 2)
	require.NoError(t, err)
	require.Len(t, limited, 2)
}
