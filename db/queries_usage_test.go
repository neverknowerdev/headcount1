package db

import (
	"agent-orchestrator/db/models"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// usageFixture is a small task tree with a known set of ledger rows:
//
//	root            (smart)   refine 100/10, plan 200/20
//	├── research    (cheap)   spawned in refine: 30/3, one failed call 5/0
//	└── implement   (cheap)   spawned in execute: 400/40
//	    └── review  (cheap)   spawned in execute: 50/5
//	other root      (smart)   refine 1000/100
//
// plus one call in a second company that no query here may ever see.
type usageFixture struct {
	workflowFixture
	research, implement, review, other Task
	cto, coder                         int32
	day1, day2                         time.Time
}

func seedUsageFixture(t *testing.T, f workflowFixture) usageFixture {
	t.Helper()
	ctx := context.Background()
	u := usageFixture{workflowFixture: f, cto: 11, coder: 12}
	child := func(parent int32, title string) Task {
		task, err := f.q.CreateTask(ctx, Task{CompanyID: f.company.ID, SprintID: f.sprint.ID, ParentID: &parent, Title: title})
		require.NoError(t, err)
		return task
	}
	u.research = child(f.root.ID, "research")
	u.implement = child(f.root.ID, "implement")
	u.review = child(u.implement.ID, "review")
	var err error
	u.other, err = f.q.CreateTask(ctx, Task{CompanyID: f.company.ID, SprintID: f.sprint.ID, Title: "other root"})
	require.NoError(t, err)

	u.day1 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	u.day2 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	record := func(task Task, tier, workflowPhase string, agentID int32, agentName, model string, prompt, completion int, status string, at time.Time) LLMCall {
		rootID, taskID := task.RootTaskID, task.ID
		call, recordErr := f.q.RecordLLMCall(ctx, LLMCall{
			CompanyID: f.company.ID, RootTaskID: &rootID, TaskID: &taskID,
			AgentID: &agentID, AgentName: agentName, Tier: tier, Purpose: "step",
			WorkflowPhase: workflowPhase, Model: model,
			PromptTokens: prompt, CompletionTokens: completion, DurationMs: 100, Status: status, CreatedAt: at,
		})
		require.NoError(t, recordErr)
		return call
	}
	record(f.root, models.TierSmart, models.TaskPhaseRefine, u.cto, "CTO", "big", 100, 10, models.LLMCallOK, u.day1)
	record(f.root, models.TierSmart, models.TaskPhasePlan, u.cto, "CTO", "big", 200, 20, models.LLMCallOK, u.day1)
	record(u.research, models.TierCheap, models.TaskPhaseRefine, u.coder, "Coder", "small", 30, 3, models.LLMCallOK, u.day1)
	record(u.research, models.TierCheap, models.TaskPhaseRefine, u.coder, "Coder", "small", 5, 0, models.LLMCallError, u.day1)
	record(u.implement, models.TierCheap, models.TaskPhaseExecute, u.coder, "Coder", "small", 400, 40, models.LLMCallOK, u.day2)
	record(u.review, models.TierCheap, models.TaskPhaseExecute, u.coder, "Coder", "small", 50, 5, models.LLMCallOK, u.day2)
	record(u.other, models.TierSmart, models.TaskPhaseRefine, u.cto, "CTO", "big", 1000, 100, models.LLMCallOK, u.day2)

	rival := Company{Name: "Rival", ShortName: "rvl"}
	require.NoError(t, f.database.Create(&rival).Error)
	_, err = f.q.RecordLLMCall(ctx, LLMCall{CompanyID: rival.ID, Tier: models.TierSmart, Model: "big", PromptTokens: 9999, CompletionTokens: 9999, Status: models.LLMCallOK, CreatedAt: u.day1})
	require.NoError(t, err)
	return u
}

func groupsByKey(groups []UsageGroup) map[string]UsageGroup {
	out := make(map[string]UsageGroup, len(groups))
	for _, group := range groups {
		out[group.Key] = group
	}
	return out
}

func usageTotalsAreScopedToTheCompany(t *testing.T, f workflowFixture) {
	u := seedUsageFixture(t, f)
	totals, err := u.q.UsageTotals(context.Background(), UsageFilter{CompanyID: u.company.ID})
	require.NoError(t, err)
	require.Equal(t, UsageTotals{Calls: 7, FailedCalls: 1, PromptTokens: 1785, CompletionTokens: 178, DurationMs: 700}, totals)

	empty, err := u.q.UsageTotals(context.Background(), UsageFilter{CompanyID: 987654})
	require.NoError(t, err)
	require.Equal(t, UsageTotals{}, empty, "a company with no calls sums to zero, not an error")
}

func usageRollsUpOverATaskSubtree(t *testing.T, f workflowFixture) {
	u := seedUsageFixture(t, f)
	ctx := context.Background()
	sum := func(filter UsageFilter) UsageTotals {
		filter.CompanyID = u.company.ID
		totals, err := u.q.UsageTotals(ctx, filter)
		require.NoError(t, err)
		return totals
	}
	require.Equal(t, int64(300), sum(UsageFilter{TaskID: &u.root.ID}).PromptTokens, "a task alone is its own calls")
	whole := sum(UsageFilter{SubtreeOf: &u.root.ID})
	require.Equal(t, int64(785), whole.PromptTokens, "a root rolls up every level beneath it")
	require.Equal(t, int64(6), whole.Calls)
	require.Equal(t, int64(1), whole.FailedCalls)
	require.Equal(t, int64(450), sum(UsageFilter{SubtreeOf: &u.implement.ID}).PromptTokens, "a subtask rolls up its own children only")
	require.Equal(t, int64(50), sum(UsageFilter{SubtreeOf: &u.review.ID}).PromptTokens)
}

func usageBreaksDownByEveryDimension(t *testing.T, f workflowFixture) {
	u := seedUsageFixture(t, f)
	ctx := context.Background()
	scope := UsageFilter{CompanyID: u.company.ID, SubtreeOf: &u.root.ID}
	whole, err := u.q.UsageTotals(ctx, scope)
	require.NoError(t, err)

	for _, dimension := range []string{UsageByTask, UsageByRoot, UsageByPhase, UsageByAgent, UsageByModel, UsageByTier, UsageByDay} {
		groups, groupErr := u.q.UsageBy(ctx, scope, dimension)
		require.NoError(t, groupErr, dimension)
		var sum UsageTotals
		for _, group := range groups {
			sum.Calls += group.Calls
			sum.FailedCalls += group.FailedCalls
			sum.PromptTokens += group.PromptTokens
			sum.CompletionTokens += group.CompletionTokens
			sum.DurationMs += group.DurationMs
		}
		require.Equal(t, whole, sum, "the %s breakdown must add up to the total", dimension)
		for i := 1; i < len(groups); i++ {
			require.GreaterOrEqual(t, groups[i-1].PromptTokens+groups[i-1].CompletionTokens,
				groups[i].PromptTokens+groups[i].CompletionTokens, "%s groups are largest first", dimension)
		}
	}

	phases, err := u.q.UsageBy(ctx, scope, UsageByPhase)
	require.NoError(t, err)
	byPhase := groupsByKey(phases)
	require.Len(t, byPhase, 3)
	// Refinement's research subtask counts as refine, not as execute.
	require.Equal(t, int64(135), byPhase[models.TaskPhaseRefine].PromptTokens)
	require.Equal(t, int64(1), byPhase[models.TaskPhaseRefine].FailedCalls)
	require.Equal(t, int64(200), byPhase[models.TaskPhasePlan].PromptTokens)
	require.Equal(t, int64(450), byPhase[models.TaskPhaseExecute].PromptTokens)

	// Within one phase, smart and cheap spend separate cleanly.
	refine := scope
	refine.WorkflowPhase = models.TaskPhaseRefine
	tiers, err := u.q.UsageBy(ctx, refine, UsageByTier)
	require.NoError(t, err)
	byTier := groupsByKey(tiers)
	require.Equal(t, int64(100), byTier[models.TierSmart].PromptTokens)
	require.Equal(t, int64(35), byTier[models.TierCheap].PromptTokens)

	agents, err := u.q.UsageBy(ctx, scope, UsageByAgent)
	require.NoError(t, err)
	require.Len(t, agents, 2)
	require.Equal(t, "Coder", agents[0].Label, "the agent that spent most comes first")
	require.Equal(t, int64(485), agents[0].PromptTokens)
	require.Equal(t, "CTO", agents[1].Label)

	models_, err := u.q.UsageBy(ctx, scope, UsageByModel)
	require.NoError(t, err)
	byModel := groupsByKey(models_)
	require.Equal(t, int64(300), byModel["big"].PromptTokens)
	require.Equal(t, int64(485), byModel["small"].PromptTokens)

	tasks, err := u.q.UsageBy(ctx, scope, UsageByTask)
	require.NoError(t, err)
	require.Len(t, tasks, 4)
	require.Contains(t, tasks[0].Label, "implement")
	require.Contains(t, tasks[0].Label, u.implement.RefKey, "a task's label carries its ref key")

	days, err := u.q.UsageBy(ctx, scope, UsageByDay)
	require.NoError(t, err)
	byDay := groupsByKey(days)
	require.Len(t, byDay, 2)
	require.Equal(t, int64(335), byDay["2026-10-05"].PromptTokens)
	require.Equal(t, int64(450), byDay["2026-10-06"].PromptTokens)

	roots, err := u.q.UsageBy(ctx, UsageFilter{CompanyID: u.company.ID}, UsageByRoot)
	require.NoError(t, err)
	require.Len(t, roots, 2)
	require.Contains(t, roots[0].Label, "other root")
	require.Equal(t, int64(1000), roots[0].PromptTokens)

	_, err = u.q.UsageBy(ctx, scope, "nonsense")
	require.Error(t, err)
}

func usageFiltersNarrowTheLedger(t *testing.T, f workflowFixture) {
	u := seedUsageFixture(t, f)
	ctx := context.Background()
	sum := func(filter UsageFilter) int64 {
		filter.CompanyID = u.company.ID
		totals, err := u.q.UsageTotals(ctx, filter)
		require.NoError(t, err)
		return totals.PromptTokens
	}
	require.Equal(t, int64(1300), sum(UsageFilter{AgentID: &u.cto}))
	require.Equal(t, int64(485), sum(UsageFilter{Model: "small"}))
	require.Equal(t, int64(1300), sum(UsageFilter{Tier: models.TierSmart}))
	require.Equal(t, int64(1135), sum(UsageFilter{WorkflowPhase: models.TaskPhaseRefine}))

	// From is inclusive and To exclusive, so adjacent ranges never overlap.
	midnight := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	require.Equal(t, int64(335), sum(UsageFilter{To: &midnight}))
	require.Equal(t, int64(1450), sum(UsageFilter{From: &midnight}))
	require.Equal(t, int64(0), sum(UsageFilter{From: &u.day2, To: &u.day2}))
	require.Equal(t, int64(1450), sum(UsageFilter{From: &u.day2}))
}

func usageListsTheCallsBehindARow(t *testing.T, f workflowFixture) {
	u := seedUsageFixture(t, f)
	ctx := context.Background()
	scope := UsageFilter{CompanyID: u.company.ID, SubtreeOf: &u.root.ID}

	all, err := u.q.ListLLMCalls(ctx, scope, 0, 0)
	require.NoError(t, err)
	require.Len(t, all, 6)
	for i := 1; i < len(all); i++ {
		require.Greater(t, all[i-1].ID, all[i].ID, "calls are listed newest first")
	}
	require.Equal(t, u.review.ID, *all[0].TaskID)
	require.Equal(t, "small", all[0].Model)

	firstPage, err := u.q.ListLLMCalls(ctx, scope, 0, 4)
	require.NoError(t, err)
	require.Len(t, firstPage, 4)
	secondPage, err := u.q.ListLLMCalls(ctx, scope, firstPage[3].ID, 4)
	require.NoError(t, err)
	require.Len(t, secondPage, 2, "paging continues below the last ID seen")
	require.Equal(t, all[4].ID, secondPage[0].ID)

	failed, err := u.q.ListLLMCalls(ctx, UsageFilter{CompanyID: u.company.ID, TaskID: &u.research.ID}, 0, 0)
	require.NoError(t, err)
	require.Len(t, failed, 2)
	require.Equal(t, models.LLMCallError, failed[0].Status)

	one, err := u.q.GetLLMCall(ctx, all[0].ID)
	require.NoError(t, err)
	require.Equal(t, all[0].ID, one.ID)
	require.Equal(t, "Coder", one.AgentName)
}

var usageRepositoryTests = []struct {
	name string
	run  func(*testing.T, workflowFixture)
}{
	{"TotalsAreScopedToTheCompany", usageTotalsAreScopedToTheCompany},
	{"RollsUpOverATaskSubtree", usageRollsUpOverATaskSubtree},
	{"BreaksDownByEveryDimension", usageBreaksDownByEveryDimension},
	{"FiltersNarrowTheLedger", usageFiltersNarrowTheLedger},
	{"ListsTheCallsBehindARow", usageListsTheCallsBehindARow},
}

func TestUsageLedger(t *testing.T) {
	for _, test := range usageRepositoryTests {
		t.Run(test.name, func(t *testing.T) { test.run(t, newSQLiteWorkflowFixture(t)) })
	}
}

// TestPostgresUsageLedger is named for the Postgres CI job (^TestPostgres).
func TestPostgresUsageLedger(t *testing.T) {
	for _, test := range usageRepositoryTests {
		t.Run(test.name, func(t *testing.T) { test.run(t, newPostgresWorkflowFixture(t)) })
	}
}
