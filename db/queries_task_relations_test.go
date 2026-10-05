package db

import (
	"agent-orchestrator/db/migrations"
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func relationFixture(t *testing.T) (*Queries, Company, Task, Task, Task) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := Company{Name: "Acme", ShortName: "acme"}
	require.NoError(t, database.Create(&company).Error)
	sprint := Sprint{CompanyID: company.ID, Name: "Sprint"}
	require.NoError(t, database.Create(&sprint).Error)
	first := Task{CompanyID: company.ID, SprintID: sprint.ID, Title: "First", Status: TaskStatusDone}
	second := Task{CompanyID: company.ID, SprintID: sprint.ID, Title: "Second", Status: TaskStatusTodo}
	third := Task{CompanyID: company.ID, SprintID: sprint.ID, Title: "Third", Status: TaskStatusBacklog}
	require.NoError(t, database.Create(&first).Error)
	require.NoError(t, database.Create(&second).Error)
	require.NoError(t, database.Create(&third).Error)
	return New(database), company, first, second, third
}

func TestTaskRelationsGateAndSummarize(t *testing.T) {
	q, _, done, dependent, related := relationFixture(t)
	ctx := context.Background()

	created, err := q.CreateTaskRelation(ctx, TaskRelation{SourceTaskID: dependent.ID, TargetTaskID: done.ID, Kind: TaskRelationDependsOn})
	require.NoError(t, err)
	require.NotZero(t, created.ID)
	ready, blockers, err := q.CanStartTask(ctx, dependent.ID)
	require.NoError(t, err)
	require.True(t, ready)
	require.Empty(t, blockers)

	_, err = q.CreateTaskRelation(ctx, TaskRelation{SourceTaskID: dependent.ID, TargetTaskID: related.ID, Kind: TaskRelationRelatedTo})
	require.NoError(t, err)
	summaries, err := q.ListTaskRelationSummaries(ctx, []int32{dependent.ID, done.ID, related.ID})
	require.NoError(t, err)
	require.Len(t, summaries[dependent.ID].DependsOn, 1)
	require.Empty(t, summaries[dependent.ID].BlockedBy)
	require.Len(t, summaries[done.ID].Blocks, 1)
	require.Len(t, summaries[related.ID].RelatedTo, 1)
}

func TestTaskRelationsRejectCyclesAndCrossCompany(t *testing.T) {
	q, company, first, second, third := relationFixture(t)
	ctx := context.Background()

	_, err := q.CreateTaskRelation(ctx, TaskRelation{CompanyID: company.ID, SourceTaskID: first.ID, TargetTaskID: second.ID, Kind: TaskRelationDependsOn})
	require.NoError(t, err)
	_, err = q.CreateTaskRelation(ctx, TaskRelation{CompanyID: company.ID, SourceTaskID: second.ID, TargetTaskID: first.ID, Kind: TaskRelationDependsOn})
	require.Error(t, err)

	otherCompany := Company{Name: "Other", ShortName: "other"}
	require.NoError(t, q.db.Create(&otherCompany).Error)
	foreign := Task{CompanyID: otherCompany.ID, Title: "Foreign", Status: TaskStatusTodo}
	require.NoError(t, q.db.Create(&foreign).Error)
	_, err = q.CreateTaskRelation(ctx, TaskRelation{SourceTaskID: third.ID, TargetTaskID: foreign.ID, Kind: TaskRelationDependsOn})
	require.Error(t, err)
}

func TestBlockingDependenciesUpdateWithTargetStatus(t *testing.T) {
	q, _, prerequisite, dependent, _ := relationFixture(t)
	ctx := context.Background()
	prerequisite.Status = TaskStatusInProgress
	_, err := q.UpdateTask(ctx, prerequisite)
	require.NoError(t, err)
	_, err = q.CreateTaskRelation(ctx, TaskRelation{SourceTaskID: dependent.ID, TargetTaskID: prerequisite.ID, Kind: TaskRelationDependsOn})
	require.NoError(t, err)
	ready, blockers, err := q.CanStartTask(ctx, dependent.ID)
	require.NoError(t, err)
	require.False(t, ready)
	require.Len(t, blockers, 1)
}
