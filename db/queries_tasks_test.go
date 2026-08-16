package db

import (
	"agent-orchestrator/db/migrations"
	"context"
	"strconv"
	"testing"

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

func TestMigrateDropAgentConfigNames(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	require.NoError(t, database.Exec("ALTER TABLE tasks ADD COLUMN agent_config_name TEXT").Error)
	require.NoError(t, database.Exec("ALTER TABLE runs ADD COLUMN agent_config_name TEXT").Error)

	require.NoError(t, New(database).MigrateDropAgentConfigNames(context.Background()))

	for _, table := range []string{"tasks", "runs"} {
		var columns []struct{ Name string }
		require.NoError(t, database.Raw("PRAGMA table_info("+table+")").Scan(&columns).Error)
		for _, column := range columns {
			require.NotEqual(t, "agent_config_name", column.Name, table)
		}
	}
}
