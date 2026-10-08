package workflow

import (
	"encoding/json"
	"testing"
	"time"

	"agent-orchestrator/db/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// managedRoot is a root task in progress in the given phase, ready for a step.
func managedRoot(taskType, phase string) Snapshot {
	return Snapshot{
		Task: Task{ID: 1, IsRoot: true, Type: taskType, Mode: models.TaskModeManaged,
			Status: models.TaskStatusInProgress, Phase: phase},
		Model: ModelReady, WorkspaceFree: true, Now: testNow,
		Roles: []string{"CEO", "CTO", "Coder", "QA", "QA Lead"},
	}
}

// directTask is a direct subtask of the given type, in progress.
func directTask(taskType string) Snapshot {
	return Snapshot{
		Task: Task{ID: 5, Depth: 1, Type: taskType, Mode: models.TaskModeDirect,
			Status: models.TaskStatusInProgress, Phase: models.TaskPhaseExecute},
		Model: ModelReady, WorkspaceFree: true, Now: testNow,
	}
}

func (s Snapshot) with(change func(*Snapshot)) Snapshot {
	change(&s)
	return s
}

func stepKinds(t *Transition) []string {
	kinds := make([]string, 0, len(t.Steps))
	for _, step := range t.Steps {
		kinds = append(kinds, step.Kind)
	}
	return kinds
}

func requireApply(t *testing.T, next Next) *Transition {
	t.Helper()
	require.Equal(t, Apply, next.Kind, "expected a transition")
	require.NotNil(t, next.Transition)
	return next.Transition
}

func assertReady(t *testing.T, tr *Transition) {
	t.Helper()
	assert.Equal(t, models.TaskWaitNone, tr.Fields["waiting_on"])
	assert.Contains(t, tr.Fields, "wait_ref")
	assert.Nil(t, tr.Fields["wait_ref"])
	assert.Nil(t, tr.Fields["wait_until"])
}

func TestTasksThatAreNotStartedOrAreFinishedAreIdle(t *testing.T) {
	for _, status := range []string{models.TaskStatusBacklog, models.TaskStatusInReview, models.TaskStatusDone,
		models.TaskStatusFailed, models.TaskStatusCanceled} {
		s := managedRoot(models.TaskTypeGeneral, models.TaskPhaseRefine).with(func(s *Snapshot) { s.Task.Status = status })
		assert.Equal(t, Idle, Evaluate(s, DefaultBudgets).Kind, status)
	}
	// Blocked with nothing tracked to wait on is somebody else's state.
	blocked := managedRoot(models.TaskTypeGeneral, models.TaskPhaseRefine).with(func(s *Snapshot) { s.Task.Status = models.TaskStatusBlocked })
	assert.Equal(t, Idle, Evaluate(blocked, DefaultBudgets).Kind)
}

func TestGate(t *testing.T) {
	queued := func(mode, taskType string, prerequisites ...string) Snapshot {
		return Snapshot{Task: Task{ID: 2, Type: taskType, Mode: mode, Status: models.TaskStatusTodo,
			SmartStepsUsed: 9, AdjustCyclesUsed: 2, AttemptsUsed: 1}, Prerequisites: prerequisites, Model: ModelReady, Now: testNow}
	}

	t.Run("a managed task starts in its first phase with fresh budgets", func(t *testing.T) {
		tr := requireApply(t, Evaluate(queued(models.TaskModeManaged, models.TaskTypeCoding), DefaultBudgets))
		assert.Equal(t, models.TaskStatusInProgress, tr.Fields["status"])
		assert.Equal(t, models.TaskPhaseRefine, tr.Fields["phase"])
		assert.Equal(t, 0, tr.Fields["smart_steps_used"])
		assert.Equal(t, 0, tr.Fields["adjust_cycles_used"])
		assert.Equal(t, 0, tr.Fields["attempts_used"])
		assert.Equal(t, "", tr.Fields["result_summary"], "an earlier outcome is cleared")
		assertReady(t, tr)
		assert.Equal(t, []string{models.StepWorkflowStarted}, stepKinds(tr))
	})
	t.Run("a direct task starts in execute", func(t *testing.T) {
		tr := requireApply(t, Evaluate(queued(models.TaskModeDirect, models.TaskTypeResearch), DefaultBudgets))
		assert.Equal(t, models.TaskPhaseExecute, tr.Fields["phase"])
	})
	t.Run("all prerequisites done lets it start", func(t *testing.T) {
		tr := requireApply(t, Evaluate(queued(models.TaskModeDirect, models.TaskTypeCoding, models.TaskStatusDone, models.TaskStatusDone), DefaultBudgets))
		assert.Equal(t, models.TaskStatusInProgress, tr.Fields["status"])
	})
	t.Run("an unfinished prerequisite parks it", func(t *testing.T) {
		tr := requireApply(t, Evaluate(queued(models.TaskModeDirect, models.TaskTypeCoding, models.TaskStatusDone, models.TaskStatusInProgress), DefaultBudgets))
		assert.Equal(t, map[string]interface{}{"status": models.TaskStatusDependsOnTask}, tr.Fields)
		assert.Empty(t, tr.Steps)
	})
	t.Run("a parked task stays parked while it waits", func(t *testing.T) {
		s := queued(models.TaskModeDirect, models.TaskTypeCoding, models.TaskStatusTodo)
		s.Task.Status = models.TaskStatusDependsOnTask
		assert.Equal(t, Idle, Evaluate(s, DefaultBudgets).Kind)
	})
	t.Run("a parked task starts once its prerequisite is done", func(t *testing.T) {
		s := queued(models.TaskModeDirect, models.TaskTypeCoding, models.TaskStatusDone)
		s.Task.Status = models.TaskStatusDependsOnTask
		assert.Equal(t, models.TaskStatusInProgress, requireApply(t, Evaluate(s, DefaultBudgets)).Fields["status"])
	})
	for _, bad := range []string{models.TaskStatusFailed, models.TaskStatusCanceled} {
		t.Run("a "+bad+" prerequisite cancels it", func(t *testing.T) {
			tr := requireApply(t, Evaluate(queued(models.TaskModeDirect, models.TaskTypeCoding, models.TaskStatusDone, bad), DefaultBudgets))
			assert.Equal(t, models.TaskStatusCanceled, tr.Fields["status"])
			assert.Equal(t, models.TaskResultPrerequisiteFailed, tr.Fields["result_reason"])
			assert.Equal(t, []string{models.StepFinished}, stepKinds(tr))
		})
	}
}

func TestManagedTaskAsksForASmartStep(t *testing.T) {
	next := Evaluate(managedRoot(models.TaskTypeCoding, models.TaskPhaseDesign), DefaultBudgets)
	require.Equal(t, SmartStep, next.Kind)
	assert.Equal(t, models.TaskPhaseDesign, next.Phase)
	assert.Equal(t, RoleCTO, next.Role, "the CTO speaks in a coding task's design phase")
	assert.Equal(t, []string{ToolAskQuestions, ToolAskHuman, ToolFinishDesign}, toolNames(next.Tools))

	next = Evaluate(managedRoot(models.TaskTypeResearch, models.TaskPhaseRefine), DefaultBudgets)
	require.Equal(t, SmartStep, next.Kind)
	assert.Empty(t, next.Role, "the task's own agent speaks")

	t.Run("a task in progress with no phase begins at the first", func(t *testing.T) {
		tr := requireApply(t, Evaluate(managedRoot(models.TaskTypeCoding, ""), DefaultBudgets))
		assert.Equal(t, models.TaskPhaseRefine, tr.Fields["phase"])
	})
}

func TestModelNotReadyBecomesAVisibleWait(t *testing.T) {
	for _, tc := range []struct {
		status ModelStatus
		wait   string
	}{{ModelVaultLocked, models.TaskWaitVault}, {ModelNotConfigured, models.TaskWaitConfig}} {
		for name, base := range map[string]Snapshot{
			"managed": managedRoot(models.TaskTypeGeneral, models.TaskPhaseRefine),
			"direct":  directTask(models.TaskTypeResearch),
		} {
			s := base.with(func(s *Snapshot) { s.Model = tc.status; s.ModelDetail = "why" })
			tr := requireApply(t, Evaluate(s, DefaultBudgets))
			assert.Equal(t, tc.wait, tr.Fields["waiting_on"], name)
			assert.Equal(t, "why", tr.Fields["wait_detail"], name)
			assert.NotContains(t, tr.Fields, "attempts_used", "not counted against the task")
			assert.False(t, tr.StartRun)

			waiting := s.with(func(s *Snapshot) { s.Task.WaitingOn = tc.wait })
			assert.Equal(t, Idle, Evaluate(waiting, DefaultBudgets).Kind, "%s still waits while the model is unavailable", name)
			ready := waiting.with(func(s *Snapshot) { s.Model = ModelReady })
			assertReady(t, requireApply(t, Evaluate(ready, DefaultBudgets)))
		}
	}
}

func TestSmartStepBudget(t *testing.T) {
	spent := func(s Snapshot) Snapshot {
		return s.with(func(s *Snapshot) { s.Task.SmartStepsUsed = DefaultBudgets.MaxSmartSteps })
	}
	t.Run("a root that runs out asks the human and gets fresh budgets", func(t *testing.T) {
		tr := requireApply(t, Evaluate(spent(managedRoot(models.TaskTypeGeneral, models.TaskPhasePlan)), DefaultBudgets))
		assert.Equal(t, models.TaskStatusBlocked, tr.Fields["status"])
		assert.Equal(t, models.TaskWaitHuman, tr.Fields["waiting_on"])
		assert.Equal(t, models.TaskPhaseAdjust, tr.Fields["phase"], "it continues by re-planning once answered")
		assert.Equal(t, 0, tr.Fields["smart_steps_used"])
		assert.Contains(t, tr.AskHuman, "40 smart steps")
		assert.Equal(t, []string{models.StepHumanQuestion}, stepKinds(tr))
	})
	t.Run("a subtask that runs out fails up to its parent", func(t *testing.T) {
		s := spent(managedRoot(models.TaskTypeGeneral, models.TaskPhasePlan)).with(func(s *Snapshot) { s.Task.IsRoot = false })
		tr := requireApply(t, Evaluate(s, DefaultBudgets))
		assert.Equal(t, models.TaskStatusFailed, tr.Fields["status"])
		assert.Equal(t, models.TaskResultBudgetExhausted, tr.Fields["result_reason"])
		assert.Empty(t, tr.AskHuman, "only a root asks the human on its own")
		assert.Equal(t, "", tr.Fields["phase"])
	})
}

func TestWaits(t *testing.T) {
	waiting := func(on string) Snapshot {
		return managedRoot(models.TaskTypeGeneral, models.TaskPhaseRefine).with(func(s *Snapshot) { s.Task.WaitingOn = on })
	}

	t.Run("human: waits until the answer arrives", func(t *testing.T) {
		s := waiting(models.TaskWaitHuman).with(func(s *Snapshot) { s.Task.Status = models.TaskStatusBlocked })
		assert.Equal(t, Idle, Evaluate(s, DefaultBudgets).Kind)
		answer := "use eu-west"
		tr := requireApply(t, Evaluate(s.with(func(s *Snapshot) { s.HumanAnswer = &answer }), DefaultBudgets))
		assert.Equal(t, models.TaskStatusInProgress, tr.Fields["status"], "the root is unblocked")
		assertReady(t, tr)
		require.Equal(t, []string{models.StepHumanAnswer}, stepKinds(tr))
		assert.Equal(t, "use eu-west", tr.Steps[0].Result)
		assert.NotContains(t, tr.Fields, "phase", "it continues in the phase that asked")
	})
	t.Run("operator: parked until the user reruns", func(t *testing.T) {
		s := waiting(models.TaskWaitOperator).with(func(s *Snapshot) { s.Task.Status = models.TaskStatusBlocked })
		assert.Equal(t, Idle, Evaluate(s, DefaultBudgets).Kind)
	})
	t.Run("backoff: waits out the delay", func(t *testing.T) {
		later, earlier := testNow.Add(time.Second), testNow.Add(-time.Second)
		assert.Equal(t, Idle, Evaluate(waiting(models.TaskWaitBackoff).with(func(s *Snapshot) { s.Task.WaitUntil = &later }), DefaultBudgets).Kind)
		assertReady(t, requireApply(t, Evaluate(waiting(models.TaskWaitBackoff).with(func(s *Snapshot) { s.Task.WaitUntil = &earlier }), DefaultBudgets)))
		assertReady(t, requireApply(t, Evaluate(waiting(models.TaskWaitBackoff), DefaultBudgets)))
	})
	t.Run("workspace: waits for the worktree", func(t *testing.T) {
		s := directTask(models.TaskTypeCoding).with(func(s *Snapshot) { s.Task.WaitingOn = models.TaskWaitWorkspace; s.WorkspaceFree = false })
		assert.Equal(t, Idle, Evaluate(s, DefaultBudgets).Kind)
		assertReady(t, requireApply(t, Evaluate(s.with(func(s *Snapshot) { s.WorkspaceFree = true }), DefaultBudgets)))
	})
	t.Run("subtasks: waits while any is unfinished", func(t *testing.T) {
		s := waiting(models.TaskWaitSubtasks).with(func(s *Snapshot) {
			s.Children = []Child{{ID: 2, Question: true, Status: models.TaskStatusDone}, {ID: 3, Question: true, Status: models.TaskStatusInProgress}}
		})
		assert.Equal(t, Idle, Evaluate(s, DefaultBudgets).Kind)
	})
	t.Run("subtasks: answered questions return the task to its phase", func(t *testing.T) {
		s := waiting(models.TaskWaitSubtasks).with(func(s *Snapshot) {
			// An unanswerable question is still an answer: the smart model decides.
			s.Children = []Child{{ID: 2, Question: true, Status: models.TaskStatusDone},
				{ID: 3, Question: true, Status: models.TaskStatusFailed, ResultReason: models.TaskResultCannotComplete}}
		})
		tr := requireApply(t, Evaluate(s, DefaultBudgets))
		assertReady(t, tr)
		assert.NotContains(t, tr.Fields, "phase")
		assert.Equal(t, []string{models.StepSubtaskFinished}, stepKinds(tr))
	})
}

func TestDirectTaskStartsAnExecutor(t *testing.T) {
	t.Run("a reader starts at once without the worktree", func(t *testing.T) {
		s := directTask(models.TaskTypeResearch).with(func(s *Snapshot) { s.WorkspaceFree = false })
		tr := requireApply(t, Evaluate(s, DefaultBudgets))
		assert.True(t, tr.StartRun)
		assert.False(t, tr.ClaimWorkspace, "readers work in a scratch directory and run in parallel")
		assert.Equal(t, models.TaskWaitRun, tr.Fields["waiting_on"])
		assert.Equal(t, Inc(1), tr.Fields["attempts_used"])
		assert.Equal(t, []string{models.StepRunStarted}, stepKinds(tr))
	})
	t.Run("a writer claims the worktree", func(t *testing.T) {
		for _, taskType := range []string{models.TaskTypeCoding, models.TaskTypeGeneral} {
			tr := requireApply(t, Evaluate(directTask(taskType), DefaultBudgets))
			assert.True(t, tr.StartRun, taskType)
			assert.True(t, tr.ClaimWorkspace, taskType)
		}
	})
	t.Run("a writer waits when another holds the worktree", func(t *testing.T) {
		tr := requireApply(t, Evaluate(directTask(models.TaskTypeCoding).with(func(s *Snapshot) { s.WorkspaceFree = false }), DefaultBudgets))
		assert.False(t, tr.StartRun)
		assert.Equal(t, models.TaskWaitWorkspace, tr.Fields["waiting_on"])
	})
	t.Run("no attempts left fails the task", func(t *testing.T) {
		s := directTask(models.TaskTypeResearch).with(func(s *Snapshot) { s.Task.AttemptsUsed = DefaultBudgets.MaxExecutorAttempts })
		tr := requireApply(t, Evaluate(s, DefaultBudgets))
		assert.False(t, tr.StartRun)
		assert.Equal(t, models.TaskStatusFailed, tr.Fields["status"])
		assert.Equal(t, models.TaskResultRunError, tr.Fields["result_reason"])
	})
}

func TestDirectTaskResolvesItsRun(t *testing.T) {
	waitingOnRun := func(taskType string, run *Run) Snapshot {
		return directTask(taskType).with(func(s *Snapshot) {
			s.Task.WaitingOn = models.TaskWaitRun
			s.Task.AttemptsUsed = 1
			s.Run = run
		})
	}
	for _, status := range []string{RunRunning, RunPaused, RunResuming} {
		assert.Equal(t, Idle, Evaluate(waitingOnRun(models.TaskTypeCoding, &Run{ID: 9, Status: status}), DefaultBudgets).Kind, status)
	}

	t.Run("a done report finishes the task and frees the worktree", func(t *testing.T) {
		report := &Report{Status: ReportDone, Summary: "added the table", Details: "see migration 12",
			Evidence: []string{"go test ok"}, OpenQuestions: []string{"index needed?"}}
		tr := requireApply(t, Evaluate(waitingOnRun(models.TaskTypeCoding, &Run{ID: 9, Status: RunCompleted, Report: report}), DefaultBudgets))
		assert.Equal(t, models.TaskStatusDone, tr.Fields["status"])
		assert.Equal(t, "", tr.Fields["result_reason"])
		assert.Equal(t, "added the table", tr.Fields["result_summary"])
		assert.Contains(t, tr.Fields["result_details"], "see migration 12")
		assert.Contains(t, tr.Fields["result_details"], "Open questions:\n- index needed?")
		assert.JSONEq(t, `["go test ok"]`, tr.Fields["result_evidence"].(string))
		assert.Nil(t, tr.Fields["run_id"])
		assert.Contains(t, tr.Fields, "run_id")
		assert.True(t, tr.ReleaseWorkspace)
		assertReady(t, tr)
		assert.Equal(t, []string{models.StepRunFinished, models.StepFinished}, stepKinds(tr))
		assert.Equal(t, int32(9), *tr.Steps[0].RunID)
	})
	t.Run("a reader has no worktree to free", func(t *testing.T) {
		tr := requireApply(t, Evaluate(waitingOnRun(models.TaskTypeResearch, &Run{ID: 9, Status: RunCompleted, Report: &Report{Status: ReportDone, Summary: "s"}}), DefaultBudgets))
		assert.False(t, tr.ReleaseWorkspace)
	})
	t.Run("a review carries its verdict and still finishes done", func(t *testing.T) {
		report := &Report{Status: ReportDone, Summary: "two problems", Verdict: models.TaskVerdictChangesRequested}
		tr := requireApply(t, Evaluate(waitingOnRun(models.TaskTypeReview, &Run{ID: 9, Status: RunCompleted, Report: report}), DefaultBudgets))
		assert.Equal(t, models.TaskStatusDone, tr.Fields["status"])
		assert.Equal(t, models.TaskVerdictChangesRequested, tr.Fields["result_verdict"])
	})
	for reported, reason := range map[string]string{ReportFailed: models.TaskResultReportedFailure, ReportCannotComplete: models.TaskResultCannotComplete} {
		t.Run("a "+reported+" report fails the task with what was learned", func(t *testing.T) {
			report := &Report{Status: reported, Summary: "no such endpoint", Details: "tried v1 and v2"}
			tr := requireApply(t, Evaluate(waitingOnRun(models.TaskTypeResearch, &Run{ID: 9, Status: RunCompleted, Report: report}), DefaultBudgets))
			assert.Equal(t, models.TaskStatusFailed, tr.Fields["status"])
			assert.Equal(t, reason, tr.Fields["result_reason"])
			assert.Equal(t, "no such endpoint", tr.Fields["result_summary"])
			assert.Equal(t, "tried v1 and v2", tr.Fields["result_details"])
		})
	}
	t.Run("a session stopped by a locked vault waits for an unlock and is not an attempt", func(t *testing.T) {
		run := &Run{ID: 9, Status: RunFailed, Error: "sign in to continue", VaultLocked: true}
		s := waitingOnRun(models.TaskTypeCoding, run).with(func(s *Snapshot) { s.Task.AttemptsUsed = DefaultBudgets.MaxExecutorAttempts })
		tr := requireApply(t, Evaluate(s, DefaultBudgets))
		assert.Equal(t, models.TaskWaitVault, tr.Fields["waiting_on"])
		assert.Equal(t, "sign in to continue", tr.Fields["wait_detail"])
		assert.Equal(t, Inc(-1), tr.Fields["attempts_used"], "the attempt is given back")
		assert.NotContains(t, tr.Fields, "status", "even on the last attempt the task does not fail")
		assert.Nil(t, tr.Fields["run_id"])
		assert.True(t, tr.ReleaseWorkspace)
	})
	t.Run("a stopped session cancels the task", func(t *testing.T) {
		tr := requireApply(t, Evaluate(waitingOnRun(models.TaskTypeCoding, &Run{ID: 9, Status: RunCanceled}), DefaultBudgets))
		assert.Equal(t, models.TaskStatusCanceled, tr.Fields["status"])
		assert.Equal(t, models.TaskResultStopped, tr.Fields["result_reason"])
		assert.True(t, tr.ReleaseWorkspace)
	})
	crashes := map[string]*Run{
		"failed":                   {ID: 9, Status: RunFailed, Error: "provider returned 500"},
		"completed without report": {ID: 9, Status: RunCompleted},
		"vanished":                 nil,
	}
	for name, run := range crashes {
		t.Run("a session that "+name+" is retried while attempts remain", func(t *testing.T) {
			tr := requireApply(t, Evaluate(waitingOnRun(models.TaskTypeCoding, run), DefaultBudgets))
			assert.NotContains(t, tr.Fields, "status", "the task stays in progress")
			assertReady(t, tr)
			assert.Nil(t, tr.Fields["run_id"])
			assert.True(t, tr.ReleaseWorkspace, "the worktree is released between attempts")
			assert.Equal(t, []string{models.StepRunFinished}, stepKinds(tr))
			assert.NotEmpty(t, tr.Steps[0].Error)
		})
		t.Run("a session that "+name+" fails the task on the last attempt", func(t *testing.T) {
			s := waitingOnRun(models.TaskTypeCoding, run).with(func(s *Snapshot) { s.Task.AttemptsUsed = DefaultBudgets.MaxExecutorAttempts })
			tr := requireApply(t, Evaluate(s, DefaultBudgets))
			assert.Equal(t, models.TaskStatusFailed, tr.Fields["status"])
			assert.Equal(t, models.TaskResultRunError, tr.Fields["result_reason"])
			assert.Equal(t, []string{models.StepRunFinished, models.StepFinished}, stepKinds(tr))
		})
	}
}

// executing is a managed task whose subtasks have all finished.
func executing(taskType string, children ...Child) Snapshot {
	return managedRoot(taskType, models.TaskPhaseExecute).with(func(s *Snapshot) {
		s.Task.WaitingOn = models.TaskWaitSubtasks
		s.Children = children
	})
}

func TestExecutionResolves(t *testing.T) {
	done := func(id int32, taskType string) Child {
		return Child{ID: id, Type: taskType, Status: models.TaskStatusDone, OriginStepID: 10}
	}
	question := Child{ID: 90, Type: models.TaskTypeResearch, Status: models.TaskStatusFailed, Question: true, OriginStepID: 3}

	t.Run("everything succeeded: verify", func(t *testing.T) {
		tr := requireApply(t, Evaluate(executing(models.TaskTypeGeneral, done(2, models.TaskTypeGeneral), done(3, models.TaskTypeResearch), question), DefaultBudgets))
		assert.Equal(t, models.TaskPhaseVerify, tr.Fields["phase"])
		assertReady(t, tr)
		assert.Equal(t, []string{models.StepPhaseEntered}, stepKinds(tr))
	})
	t.Run("an unanswered question from an earlier phase is not an execution failure", func(t *testing.T) {
		tr := requireApply(t, Evaluate(executing(models.TaskTypeGeneral, done(2, models.TaskTypeGeneral), question), DefaultBudgets))
		assert.Equal(t, models.TaskPhaseVerify, tr.Fields["phase"])
	})
	t.Run("execute with nothing pending resolves the same way", func(t *testing.T) {
		s := executing(models.TaskTypeGeneral, done(2, models.TaskTypeGeneral)).with(func(s *Snapshot) { s.Task.WaitingOn = models.TaskWaitNone })
		assert.Equal(t, models.TaskPhaseVerify, requireApply(t, Evaluate(s, DefaultBudgets)).Fields["phase"])
	})
	t.Run("a failure sends the task to adjust with the whole picture", func(t *testing.T) {
		failed := Child{ID: 3, Type: models.TaskTypeCoding, Status: models.TaskStatusFailed, ResultReason: models.TaskResultReportedFailure, OriginStepID: 10}
		canceled := Child{ID: 4, Type: models.TaskTypeCoding, Status: models.TaskStatusCanceled, ResultReason: models.TaskResultPrerequisiteFailed, OriginStepID: 10, DependsOn: []int32{3}}
		tr := requireApply(t, Evaluate(executing(models.TaskTypeGeneral, done(2, models.TaskTypeGeneral), failed, canceled), DefaultBudgets))
		assert.Equal(t, models.TaskPhaseAdjust, tr.Fields["phase"])
		assert.Equal(t, Inc(1), tr.Fields["adjust_cycles_used"])
		assertReady(t, tr)
		require.Equal(t, []string{models.StepPhaseEntered}, stepKinds(tr))
		assert.Contains(t, tr.Steps[0].Result, "task 3 failed (reported_failure)")
		assert.Contains(t, tr.Steps[0].Result, "task 4 canceled (prerequisite_failed)")
	})
	t.Run("failures an earlier re-plan dealt with do not count again", func(t *testing.T) {
		old := Child{ID: 3, Type: models.TaskTypeCoding, Status: models.TaskStatusFailed, OriginStepID: 10}
		replacement := Child{ID: 6, Type: models.TaskTypeCoding, Status: models.TaskStatusDone, OriginStepID: 20}
		s := executing(models.TaskTypeGeneral, old, replacement).with(func(s *Snapshot) { s.HandledStepID = 20 })
		assert.Equal(t, models.TaskPhaseVerify, requireApply(t, Evaluate(s, DefaultBudgets)).Fields["phase"])
	})
	t.Run("out of re-plans: a root asks the human", func(t *testing.T) {
		failed := Child{ID: 3, Type: models.TaskTypeCoding, Status: models.TaskStatusFailed, OriginStepID: 10}
		s := executing(models.TaskTypeGeneral, failed).with(func(s *Snapshot) { s.Task.AdjustCyclesUsed = DefaultBudgets.MaxAdjustCycles })
		tr := requireApply(t, Evaluate(s, DefaultBudgets))
		assert.Equal(t, models.TaskStatusBlocked, tr.Fields["status"])
		assert.Equal(t, models.TaskWaitHuman, tr.Fields["waiting_on"])
		assert.Contains(t, tr.AskHuman, "re-planned 3 times")
	})
}

func TestEngineRunsTheReviewLoopForCoding(t *testing.T) {
	implementation := Child{ID: 2, Title: "Schema", Type: models.TaskTypeCoding, Status: models.TaskStatusDone, OriginStepID: 10}
	rejecting := func(id int32, round int, dependsOn int32) Child {
		return Child{ID: id, Title: "Review: Schema", Type: models.TaskTypeReview, Status: models.TaskStatusDone,
			ResultVerdict: models.TaskVerdictChangesRequested, ResultSummary: "missing index on user_id",
			ReviewRound: round, OriginStepID: 10, DependsOn: []int32{dependsOn}}
	}

	t.Run("an approved review needs nothing more", func(t *testing.T) {
		approved := rejecting(3, 0, 2)
		approved.ResultVerdict = models.TaskVerdictApproved
		assert.Equal(t, models.TaskPhaseVerify, requireApply(t, Evaluate(executing(models.TaskTypeCoding, implementation, approved), DefaultBudgets)).Fields["phase"])
	})
	t.Run("requested changes start a fix and a new review with no smart call", func(t *testing.T) {
		tr := requireApply(t, Evaluate(executing(models.TaskTypeCoding, implementation, rejecting(3, 0, 2)), DefaultBudgets))
		assert.NotContains(t, tr.Fields, "phase", "the task stays in execute")
		assert.Equal(t, models.TaskWaitSubtasks, tr.Fields["waiting_on"])
		require.Len(t, tr.NewTasks, 2)
		fix, review := tr.NewTasks[0], tr.NewTasks[1]
		assert.Equal(t, models.TaskTypeCoding, fix.Type)
		assert.Equal(t, RoleCoder, fix.Role)
		assert.Equal(t, 1, fix.ReviewRound)
		assert.Equal(t, []int32{3}, fix.DependsOnIDs, "the fix follows the review it answers")
		assert.Contains(t, fix.Instructions, "missing index on user_id")
		assert.Equal(t, models.TaskTypeReview, review.Type)
		assert.Equal(t, RoleQA, review.Role)
		assert.Equal(t, []string{fix.Key}, review.DependsOnKeys, "the new review follows the fix")
		assert.Equal(t, "Review: Schema", review.Title)
		assert.Equal(t, []string{models.StepReviewRound}, stepKinds(tr))
	})
	t.Run("a review already being fixed is not fixed twice", func(t *testing.T) {
		fix := Child{ID: 4, Type: models.TaskTypeCoding, Status: models.TaskStatusDone, ReviewRound: 1, OriginStepID: 11, DependsOn: []int32{3}}
		approved := rejecting(5, 1, 4)
		approved.ResultVerdict = models.TaskVerdictApproved
		tr := requireApply(t, Evaluate(executing(models.TaskTypeCoding, implementation, rejecting(3, 0, 2), fix, approved), DefaultBudgets))
		assert.Empty(t, tr.NewTasks)
		assert.Equal(t, models.TaskPhaseVerify, tr.Fields["phase"])
	})
	t.Run("after the allowed rounds a smart model takes over", func(t *testing.T) {
		last := rejecting(7, DefaultBudgets.MaxReviewRounds, 6)
		tr := requireApply(t, Evaluate(executing(models.TaskTypeCoding, implementation, last), DefaultBudgets))
		assert.Empty(t, tr.NewTasks)
		assert.Equal(t, models.TaskPhaseAdjust, tr.Fields["phase"])
		assert.Contains(t, tr.Steps[0].Result, "review 7 still requests changes")
	})
	t.Run("other task types have no engine loop: requested changes go to adjust", func(t *testing.T) {
		tr := requireApply(t, Evaluate(executing(models.TaskTypeGeneral, implementation, rejecting(3, 0, 2)), DefaultBudgets))
		assert.Empty(t, tr.NewTasks)
		assert.Equal(t, models.TaskPhaseAdjust, tr.Fields["phase"])
	})
}

func parse(t *testing.T, s Snapshot, tool, args string) Action {
	t.Helper()
	action, err := ParseAction(s.Task.Phase, tool, json.RawMessage(args), s.ToolContext(DefaultBudgets))
	require.NoError(t, err)
	return action
}

func TestApplyActionAlwaysJournalsTheCallAndCountsTheStep(t *testing.T) {
	s := managedRoot(models.TaskTypeGeneral, models.TaskPhaseAdjust).with(func(s *Snapshot) { s.Task.AttemptsUsed = 2 })
	args := `{"scope":"root"}`
	tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolGetDecisionTree, args), args)
	require.Equal(t, []string{models.StepSmartCall}, stepKinds(tr))
	assert.Equal(t, ToolGetDecisionTree, tr.Steps[0].ToolName)
	assert.Equal(t, args, tr.Steps[0].ToolArgs)
	assert.Equal(t, models.TaskPhaseAdjust, tr.Steps[0].Phase)
	assert.Equal(t, Inc(1), tr.Fields["smart_steps_used"])
	assert.Equal(t, 0, tr.Fields["attempts_used"], "a call that worked clears the failure streak")
	assert.NotContains(t, tr.Fields, "phase", "a read-only tool changes nothing else")
	assert.Empty(t, tr.NewTasks)
}

func TestApplyAskQuestions(t *testing.T) {
	s := managedRoot(models.TaskTypeCoding, models.TaskPhaseRefine)
	args := `{"questions":[{"question":"How is auth done today?\nMore detail.","context":"look in server/"},{"question":"Do the tests pass?","kind":"review"}],"decisions":[]}`
	tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolAskQuestions, args), args)
	assert.Equal(t, models.TaskWaitSubtasks, tr.Fields["waiting_on"])
	assert.NotContains(t, tr.Fields, "phase")
	require.Len(t, tr.NewTasks, 2)
	assert.Equal(t, "How is auth done today?", tr.NewTasks[0].Title, "the title is the question's first line")
	assert.Contains(t, tr.NewTasks[0].Instructions, "Context:\nlook in server/")
	assert.Equal(t, models.TaskTypeResearch, tr.NewTasks[0].Type)
	assert.Equal(t, models.TaskTypeReview, tr.NewTasks[1].Type)
	for _, draft := range tr.NewTasks {
		assert.Equal(t, models.TaskModeDirect, draft.Mode, "questions are answered by executors")
		assert.Empty(t, draft.DependsOnKeys, "questions are researched in parallel")
	}
}

func TestApplyAskHuman(t *testing.T) {
	args := `{"question":"Which region?","why":"decides the provider","decisions":[]}`
	root := managedRoot(models.TaskTypeGeneral, models.TaskPhasePlan)
	tr := ApplyAction(root, DefaultBudgets, parse(t, root, ToolAskHuman, args), args)
	assert.Equal(t, "Which region?", tr.AskHuman)
	assert.Equal(t, models.TaskWaitHuman, tr.Fields["waiting_on"])
	assert.Equal(t, models.TaskStatusBlocked, tr.Fields["status"], "a root shows as blocked while it waits for you")
	assert.Equal(t, []string{models.StepSmartCall, models.StepHumanQuestion}, stepKinds(tr))

	subtask := root.with(func(s *Snapshot) { s.Task.IsRoot = false })
	tr = ApplyAction(subtask, DefaultBudgets, parse(t, subtask, ToolAskHuman, args), args)
	assert.NotContains(t, tr.Fields, "status", "a subtask stays in progress; the question shows on the root")
}

func TestApplyPhaseFinishersMoveAlongTheTemplate(t *testing.T) {
	t.Run("refinement writes the spec and goes to design for coding", func(t *testing.T) {
		s := managedRoot(models.TaskTypeCoding, models.TaskPhaseRefine)
		args := `{"spec":"Add SSO","definition_of_done":["login via Okta works","old login still works"],"decisions":[{"title":"SAML","decision":"use SAML","rationale":"IdP supports it","alternatives":["OIDC"],"assumption":true}]}`
		tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolFinishRefinement, args), args)
		assert.Equal(t, "Add SSO", tr.Fields["refined_description"])
		assert.JSONEq(t, `[{"id":1,"text":"login via Okta works","status":"pending"},{"id":2,"text":"old login still works","status":"pending"}]`, tr.Fields["acceptance_criteria"].(string))
		assert.Equal(t, models.TaskPhaseDesign, tr.Fields["phase"])
		assert.Equal(t, []string{models.StepSmartCall, models.StepPhaseEntered}, stepKinds(tr))
		require.Len(t, tr.Decisions, 1)
		assert.Equal(t, models.DecisionKindAssumption, tr.Decisions[0].Kind())
		assert.Equal(t, []string{"OIDC"}, tr.Decisions[0].Alternatives)
	})
	t.Run("refinement goes straight to plan for other types", func(t *testing.T) {
		s := managedRoot(models.TaskTypeResearch, models.TaskPhaseRefine)
		args := `{"spec":"s","definition_of_done":["a"],` + oneDecision + `}`
		assert.Equal(t, models.TaskPhasePlan, ApplyAction(s, DefaultBudgets, parse(t, s, ToolFinishRefinement, args), args).Fields["phase"])
	})
	t.Run("design is stored and leads to the test plan", func(t *testing.T) {
		s := managedRoot(models.TaskTypeCoding, models.TaskPhaseDesign)
		args := `{"design":"Add a SAML handler",` + oneDecision + `}`
		tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolFinishDesign, args), args)
		assert.Equal(t, "Add a SAML handler", tr.Fields["design"])
		assert.Equal(t, models.TaskPhaseTestPlan, tr.Fields["phase"])
	})
	t.Run("the test plan is stored and leads to planning", func(t *testing.T) {
		s := managedRoot(models.TaskTypeCoding, models.TaskPhaseTestPlan)
		args := `{"test_scenarios":["valid assertion logs in"],` + oneDecision + `}`
		tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolFinishTestPlan, args), args)
		assert.JSONEq(t, `[{"id":1,"text":"valid assertion logs in","status":"pending"}]`, tr.Fields["test_cases"].(string))
		assert.Equal(t, models.TaskPhasePlan, tr.Fields["phase"])
	})
}

const planArgs = `{"tasks":[
	{"key":"db","title":"Schema","instructions":"add table","done_when":"migrates","type":"coding"},
	{"key":"api","title":"API","instructions":"add route","done_when":"tests pass","type":"coding","role":"CTO","depends_on":["db"]},
	{"key":"docs","title":"Docs","instructions":"write them","done_when":"published","type":"general"},
	{"key":"sub","title":"Subsystem","instructions":"build it","done_when":"works","type":"coding","managed":true}],` + oneDecision + `}`

func TestApplyCreateTasks(t *testing.T) {
	byKey := func(tr *Transition) map[string]TaskDraft {
		drafts := map[string]TaskDraft{}
		for _, draft := range tr.NewTasks {
			drafts[draft.Key] = draft
		}
		return drafts
	}

	t.Run("coding plans get a review after each piece of implementation", func(t *testing.T) {
		s := managedRoot(models.TaskTypeCoding, models.TaskPhasePlan)
		tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolCreateTasks, planArgs), planArgs)
		assert.Equal(t, models.TaskPhaseExecute, tr.Fields["phase"])
		assert.Equal(t, models.TaskWaitSubtasks, tr.Fields["waiting_on"])
		drafts := byKey(tr)
		require.Len(t, drafts, 6, "four planned tasks and a review for each of the two direct coding tasks")

		assert.Equal(t, RoleCoder, drafts["db"].Role, "the default executor role")
		assert.Equal(t, "CTO", drafts["api"].Role, "a named role is kept")
		assert.Equal(t, []string{"db"}, drafts["api"].DependsOnKeys)
		assert.Contains(t, drafts["db"].Instructions, "add table")
		assert.Contains(t, drafts["db"].Instructions, "Done when:\nmigrates")
		assert.Equal(t, models.TaskModeDirect, drafts["db"].Mode)
		assert.Equal(t, models.TaskModeManaged, drafts["sub"].Mode)

		review := drafts["review:db"]
		assert.Equal(t, models.TaskTypeReview, review.Type)
		assert.Equal(t, RoleQA, review.Role)
		assert.Equal(t, []string{"db"}, review.DependsOnKeys)
		assert.Contains(t, review.Instructions, "add table")
		assert.Contains(t, drafts, "review:api")
		assert.NotContains(t, drafts, "review:docs", "only implementation is reviewed")
		assert.NotContains(t, drafts, "review:sub", "a managed subtask verifies itself")
	})
	t.Run("other types delegate exactly what was planned", func(t *testing.T) {
		s := managedRoot(models.TaskTypeGeneral, models.TaskPhasePlan)
		tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolCreateTasks, planArgs), planArgs)
		assert.Len(t, tr.NewTasks, 4)
		assert.Empty(t, byKey(tr)["db"].Role)
	})
	t.Run("from adjust it returns to execute", func(t *testing.T) {
		s := managedRoot(models.TaskTypeGeneral, models.TaskPhaseAdjust)
		tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolCreateTasks, planArgs), planArgs)
		assert.Equal(t, models.TaskPhaseExecute, tr.Fields["phase"])
	})
}

func TestApplyRetryTasks(t *testing.T) {
	s := managedRoot(models.TaskTypeGeneral, models.TaskPhaseAdjust).with(func(s *Snapshot) {
		s.Children = []Child{
			{ID: 7, Title: "Fetch prices", Instructions: "call the API", Type: models.TaskTypeResearch, Role: "Coder",
				Status: models.TaskStatusFailed, ReviewRound: 1, OriginStepID: 10},
			{ID: 8, Title: "Other", Type: models.TaskTypeGeneral, Status: models.TaskStatusDone, OriginStepID: 10},
		}
	})
	args := `{"task_ids":[7],"instructions":"use the v2 endpoint",` + oneDecision + `}`
	tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolRetryTasks, args), args)
	assert.Equal(t, models.TaskPhaseExecute, tr.Fields["phase"])
	assert.Equal(t, models.TaskWaitSubtasks, tr.Fields["waiting_on"])
	require.Len(t, tr.NewTasks, 1, "a retry is a new task; the failed one stays as the record of what happened")
	retry := tr.NewTasks[0]
	assert.Equal(t, "Fetch prices", retry.Title)
	assert.Contains(t, retry.Instructions, "call the API")
	assert.Contains(t, retry.Instructions, "use the v2 endpoint")
	assert.Equal(t, models.TaskTypeResearch, retry.Type)
	assert.Equal(t, "Coder", retry.Role)
	assert.Equal(t, 1, retry.ReviewRound)

	_, err := ParseAction(models.TaskPhaseAdjust, ToolRetryTasks, json.RawMessage(`{"task_ids":[8],"instructions":"x",`+oneDecision+`}`), s.ToolContext(DefaultBudgets))
	require.Error(t, err, "a task that succeeded cannot be retried")
}

func TestApplyFinishAdjustmentGoesToVerify(t *testing.T) {
	s := managedRoot(models.TaskTypeGeneral, models.TaskPhaseAdjust)
	args := `{"reason":"the failed part was optional",` + oneDecision + `}`
	tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolFinishAdjustment, args), args)
	assert.Equal(t, models.TaskPhaseVerify, tr.Fields["phase"])
	assert.Equal(t, []string{models.StepSmartCall, models.StepPhaseEntered}, stepKinds(tr))
}

func TestApplyEscalate(t *testing.T) {
	args := `{"reason":"no access to production",` + oneDecision + `}`
	root := managedRoot(models.TaskTypeGeneral, models.TaskPhaseAdjust)
	tr := ApplyAction(root, DefaultBudgets, parse(t, root, ToolEscalate, args), args)
	assert.Equal(t, models.TaskStatusBlocked, tr.Fields["status"])
	assert.Equal(t, models.TaskWaitHuman, tr.Fields["waiting_on"])
	assert.Contains(t, tr.AskHuman, "no access to production")
	assert.Len(t, tr.Decisions, 1, "the decision to give up is recorded too")
	assert.Equal(t, []string{models.StepSmartCall, models.StepHumanQuestion}, stepKinds(tr))

	subtask := root.with(func(s *Snapshot) { s.Task.IsRoot = false })
	tr = ApplyAction(subtask, DefaultBudgets, parse(t, subtask, ToolEscalate, args), args)
	assert.Equal(t, models.TaskStatusFailed, tr.Fields["status"])
	assert.Equal(t, "no access to production", tr.Fields["result_summary"])
	assert.Empty(t, tr.AskHuman)
}

func TestApplyFinishVerification(t *testing.T) {
	pass := `{"passed":true,"criteria":[{"criterion":"login works","passed":true,"note":"seen in test run"}],"summary":"SSO shipped",` + oneDecision + `}`
	fail := `{"passed":false,"criteria":[{"criterion":"login works","passed":false,"note":"500 on callback"}],"summary":"fix the callback",` + oneDecision + `}`

	t.Run("a passing root stops for review and publishes a coding PR", func(t *testing.T) {
		s := managedRoot(models.TaskTypeCoding, models.TaskPhaseVerify)
		tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolFinishVerification, pass), pass)
		assert.Equal(t, models.TaskStatusInReview, tr.Fields["status"])
		assert.Equal(t, "", tr.Fields["phase"])
		assert.Equal(t, "SSO shipped", tr.Fields["result_summary"])
		assert.True(t, tr.PublishPR)
		assert.JSONEq(t, `[{"id":1,"text":"login works","status":"passed","note":"seen in test run"}]`, tr.Fields["acceptance_criteria"].(string))
		assertReady(t, tr)
		assert.Equal(t, []string{models.StepSmartCall, models.StepFinished}, stepKinds(tr))
	})
	t.Run("a passing non-coding root has no PR", func(t *testing.T) {
		s := managedRoot(models.TaskTypeResearch, models.TaskPhaseVerify)
		assert.False(t, ApplyAction(s, DefaultBudgets, parse(t, s, ToolFinishVerification, pass), pass).PublishPR)
	})
	t.Run("a passing subtask is done", func(t *testing.T) {
		s := managedRoot(models.TaskTypeCoding, models.TaskPhaseVerify).with(func(s *Snapshot) { s.Task.IsRoot = false })
		tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolFinishVerification, pass), pass)
		assert.Equal(t, models.TaskStatusDone, tr.Fields["status"])
		assert.False(t, tr.PublishPR, "only the root publishes")
	})
	t.Run("a failing verdict re-plans", func(t *testing.T) {
		s := managedRoot(models.TaskTypeCoding, models.TaskPhaseVerify)
		tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolFinishVerification, fail), fail)
		assert.Equal(t, models.TaskPhaseAdjust, tr.Fields["phase"])
		assert.Equal(t, Inc(1), tr.Fields["adjust_cycles_used"])
		assert.NotContains(t, tr.Fields, "status")
		assert.Contains(t, tr.Fields["acceptance_criteria"], `"status":"failed"`)
		assert.Contains(t, tr.Steps[len(tr.Steps)-1].Result, "fix the callback")
	})
	t.Run("a failing verdict with no re-plans left escalates", func(t *testing.T) {
		s := managedRoot(models.TaskTypeCoding, models.TaskPhaseVerify).with(func(s *Snapshot) { s.Task.AdjustCyclesUsed = DefaultBudgets.MaxAdjustCycles })
		tr := ApplyAction(s, DefaultBudgets, parse(t, s, ToolFinishVerification, fail), fail)
		assert.Equal(t, models.TaskStatusBlocked, tr.Fields["status"])
		assert.Contains(t, tr.AskHuman, "fix the callback")
	})
}

func TestApplyFailure(t *testing.T) {
	s := managedRoot(models.TaskTypeGeneral, models.TaskPhasePlan)

	t.Run("the first failures back off, doubling", func(t *testing.T) {
		first := ApplyFailure(s, DefaultBudgets, FailureTransient, "HTTP 502")
		assert.Equal(t, models.TaskWaitBackoff, first.Fields["waiting_on"])
		assert.Equal(t, testNow.Add(30*time.Second), first.Fields["wait_until"])
		assert.Equal(t, Inc(1), first.Fields["attempts_used"])
		require.Equal(t, []string{models.StepSmartError}, stepKinds(first))
		assert.Equal(t, "HTTP 502", first.Steps[0].Error)
		assert.NotContains(t, first.Fields, "smart_steps_used", "a call that produced nothing does not use a step")

		second := ApplyFailure(s.with(func(s *Snapshot) { s.Task.AttemptsUsed = 1 }), DefaultBudgets, FailureTransient, "HTTP 502")
		assert.Equal(t, testNow.Add(60*time.Second), second.Fields["wait_until"])
	})
	t.Run("the failure that reaches the limit escalates instead of retrying", func(t *testing.T) {
		tr := ApplyFailure(s.with(func(s *Snapshot) { s.Task.AttemptsUsed = DefaultBudgets.MaxSmartFailures - 1 }), DefaultBudgets, FailureTransient, "no tool call")
		assert.Equal(t, models.TaskWaitHuman, tr.Fields["waiting_on"])
		assert.Contains(t, tr.AskHuman, "failed 3 times in a row")
		assert.Equal(t, []string{models.StepSmartError, models.StepHumanQuestion}, stepKinds(tr))
	})
	t.Run("a locked vault or missing model waits without counting", func(t *testing.T) {
		for kind, wait := range map[FailureKind]string{FailureVaultLocked: models.TaskWaitVault, FailureNotConfigured: models.TaskWaitConfig} {
			tr := ApplyFailure(s.with(func(s *Snapshot) { s.Task.AttemptsUsed = DefaultBudgets.MaxSmartFailures - 1 }), DefaultBudgets, kind, "sign in to continue")
			assert.Equal(t, wait, tr.Fields["waiting_on"])
			assert.Equal(t, "sign in to continue", tr.Fields["wait_detail"])
			assert.NotContains(t, tr.Fields, "attempts_used")
			assert.Empty(t, tr.AskHuman)
		}
	})
}

func TestStopAndRerun(t *testing.T) {
	root := managedRoot(models.TaskTypeCoding, models.TaskPhaseExecute).with(func(s *Snapshot) { s.Task.WaitingOn = models.TaskWaitSubtasks })
	stopped := Stop(root)
	assert.Equal(t, models.TaskResultStopped, stopped.CancelDescendants, "the work under a stopped task stops with it")
	assert.Equal(t, models.TaskStatusBlocked, stopped.Fields["status"])
	assert.Equal(t, models.TaskWaitOperator, stopped.Fields["waiting_on"])
	assert.True(t, stopped.ReleaseWorkspace)
	assert.NotContains(t, stopped.Fields, "phase", "a stopped root remembers where it was")
	assert.Equal(t, []string{models.StepStopped}, stepKinds(stopped))

	subtask := Stop(directTask(models.TaskTypeCoding))
	assert.Equal(t, models.TaskStatusCanceled, subtask.Fields["status"])
	assert.Equal(t, models.TaskResultStopped, subtask.Fields["result_reason"])
	assert.Nil(t, subtask.Fields["run_id"])

	rerun := Rerun(root, true, "use the staging database")
	assert.Equal(t, "use the staging database", rerun.Steps[0].Result)
	assert.Equal(t, models.TaskStatusInProgress, rerun.Fields["status"])
	assert.Equal(t, models.TaskPhaseAdjust, rerun.Fields["phase"], "a task with a specification re-plans from where it is")
	assert.Equal(t, 0, rerun.Fields["smart_steps_used"])
	assertReady(t, rerun)
	assert.Equal(t, models.TaskPhaseRefine, Rerun(root, false, "").Fields["phase"], "one that never got a specification starts over")
	assert.Equal(t, models.TaskPhaseExecute, Rerun(directTask(models.TaskTypeResearch), false, "").Fields["phase"])
}

// Every evaluation must make progress or be safely repeatable: applying a
// transition and evaluating again never returns the same transition forever.
// This walks a whole coding task to completion through the reducer alone.
func TestACodingTaskRunsToReviewThroughTheReducer(t *testing.T) {
	s := Snapshot{Task: Task{ID: 1, IsRoot: true, Type: models.TaskTypeCoding, Mode: models.TaskModeManaged, Status: models.TaskStatusTodo},
		Model: ModelReady, WorkspaceFree: true, Now: testNow, Roles: []string{"CTO", "Coder", "QA", "QA Lead"}}
	absorb := func(tr *Transition) {
		for column, value := range tr.Fields {
			switch column {
			case "status":
				s.Task.Status = value.(string)
			case "phase":
				s.Task.Phase = value.(string)
			case "waiting_on":
				s.Task.WaitingOn = value.(string)
			case "smart_steps_used":
				if inc, ok := value.(Inc); ok {
					s.Task.SmartStepsUsed += int(inc)
				} else {
					s.Task.SmartStepsUsed = value.(int)
				}
			}
		}
	}
	answer := func(tool, args string) {
		next := Evaluate(s, DefaultBudgets)
		require.Equal(t, SmartStep, next.Kind, "expected a smart step in %s", s.Task.Phase)
		require.Contains(t, toolNames(next.Tools), tool)
		absorb(ApplyAction(s, DefaultBudgets, parse(t, s, tool, args), args))
	}

	absorb(requireApply(t, Evaluate(s, DefaultBudgets)))
	require.Equal(t, models.TaskPhaseRefine, s.Task.Phase)
	answer(ToolFinishRefinement, `{"spec":"s","definition_of_done":["a"],`+oneDecision+`}`)
	answer(ToolFinishDesign, `{"design":"d",`+oneDecision+`}`)
	answer(ToolFinishTestPlan, `{"test_scenarios":["t"],`+oneDecision+`}`)
	answer(ToolCreateTasks, `{"tasks":[{"key":"a","title":"A","instructions":"i","done_when":"d","type":"coding"}],`+oneDecision+`}`)
	require.Equal(t, models.TaskPhaseExecute, s.Task.Phase)
	require.Equal(t, models.TaskWaitSubtasks, s.Task.WaitingOn)

	s.Children = []Child{
		{ID: 2, Type: models.TaskTypeCoding, Status: models.TaskStatusInProgress, OriginStepID: 4},
		{ID: 3, Type: models.TaskTypeReview, Status: models.TaskStatusDependsOnTask, OriginStepID: 4, DependsOn: []int32{2}},
	}
	require.Equal(t, Idle, Evaluate(s, DefaultBudgets).Kind, "it waits while work is in flight")
	s.Children[0].Status = models.TaskStatusDone
	s.Children[1].Status, s.Children[1].ResultVerdict = models.TaskStatusDone, models.TaskVerdictApproved
	absorb(requireApply(t, Evaluate(s, DefaultBudgets)))
	require.Equal(t, models.TaskPhaseVerify, s.Task.Phase)
	answer(ToolFinishVerification, `{"passed":true,"criteria":[{"criterion":"a","passed":true,"note":"n"}],"summary":"done",`+oneDecision+`}`)

	assert.Equal(t, models.TaskStatusInReview, s.Task.Status)
	assert.Equal(t, "", s.Task.Phase)
	assert.Equal(t, 5, s.Task.SmartStepsUsed, "five smart calls for the whole task")
	assert.Equal(t, Idle, Evaluate(s, DefaultBudgets).Kind, "a task in review is finished as far as the workflow goes")
}
