package engine

import (
	"context"
	"testing"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAssignRunName(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, database.AutoMigrate(&db.Company{}, &db.Agent{}, &db.Sprint{}, &db.Task{}, &db.Run{}))

	company := db.Company{Name: "Acme", ShortName: "ACME"}
	require.NoError(t, database.Create(&company).Error)
	sprint := db.Sprint{CompanyID: company.ID, Name: "Sprint 1"}
	require.NoError(t, database.Create(&sprint).Error)
	ceo := db.Agent{CompanyID: company.ID, Name: "Chief Executive Officer", ShortName: "CEO"}
	cto := db.Agent{CompanyID: company.ID, Name: "Chief Technology Officer", ShortName: "CTO"}
	require.NoError(t, database.Create(&ceo).Error)
	require.NoError(t, database.Create(&cto).Error)

	rootTask := db.Task{
		CompanyID: company.ID,
		SprintID:  sprint.ID,
		AgentID:   &ceo.ID,
		Title:     "Build the product",
		RefKey:    "ACME-2",
	}
	require.NoError(t, database.Create(&rootTask).Error)
	q := db.New(database)
	ctx := context.Background()

	rootRun := db.Run{TaskID: rootTask.ID, AgentID: ceo.ID, Status: "running"}
	require.NoError(t, database.Create(&rootRun).Error)
	rootRun.RootRunID = &rootRun.ID
	require.NoError(t, q.SetRunRootID(ctx, rootRun.ID, rootRun.ID))
	rootRun = assignRunName(ctx, q, rootTask, ceo, rootRun, nil, rootTask.ID, rootRun.ID)
	require.Equal(t, "ACME-2-CEO-1", rootRun.Name)

	secondRoot := db.Run{TaskID: rootTask.ID, AgentID: ceo.ID, Status: "running"}
	require.NoError(t, database.Create(&secondRoot).Error)
	secondRoot = assignRunName(ctx, q, rootTask, ceo, secondRoot, nil, rootTask.ID, secondRoot.ID)
	require.Equal(t, "ACME-2-CEO-2", secondRoot.Name)

	childTask := db.Task{
		CompanyID: company.ID,
		SprintID:  sprint.ID,
		AgentID:   &cto.ID,
		ParentID:  &rootTask.ID,
		Title:     "Implement the product",
		RefKey:    "ACME-2-1",
	}
	require.NoError(t, database.Create(&childTask).Error)
	parentRunID := rootRun.ID
	rootRunID := rootRun.ID
	childRun := db.Run{
		TaskID:      childTask.ID,
		AgentID:     cto.ID,
		ParentRunID: &parentRunID,
		RootRunID:   &rootRunID,
		Status:      "running",
	}
	require.NoError(t, database.Create(&childRun).Error)
	childRun = assignRunName(ctx, q, childTask, cto, childRun, &parentSession{}, rootTask.ID, rootRun.ID)
	require.Equal(t, "ACME-2-CTO-1-1", childRun.Name)

	secondChild := db.Run{
		TaskID:      childTask.ID,
		AgentID:     cto.ID,
		ParentRunID: &parentRunID,
		RootRunID:   &rootRunID,
		Status:      "running",
	}
	require.NoError(t, database.Create(&secondChild).Error)
	secondChild = assignRunName(ctx, q, childTask, cto, secondChild, &parentSession{}, rootTask.ID, rootRun.ID)
	require.Equal(t, "ACME-2-CTO-1-2", secondChild.Name)

	stored, err := q.GetRun(ctx, secondChild.ID)
	require.NoError(t, err)
	require.Equal(t, "ACME-2-CTO-1-2", stored.Name)
}
