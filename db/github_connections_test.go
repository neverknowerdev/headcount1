package db_test

import (
	"testing"
	"time"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGitHubConnectionsAllowMultipleMCPAccountsPerInstallation(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.EnsureSchema(database))

	// A work identity and a personal identity can both have access to the same
	// organisation installation. The old installation_id unique constraint made
	// the second connection overwrite the first one.
	first := db.GitHubConnection{InstallationID: 42, MCPAccountID: 10, UserID: 1, AccountLogin: "work", ConnectedAt: time.Now()}
	second := db.GitHubConnection{InstallationID: 42, MCPAccountID: 11, UserID: 1, AccountLogin: "personal", ConnectedAt: time.Now()}
	require.NoError(t, database.Create(&first).Error)
	require.NoError(t, database.Create(&second).Error)
	duplicate := db.GitHubConnection{InstallationID: 42, MCPAccountID: 10, UserID: 1, AccountLogin: "duplicate", ConnectedAt: time.Now()}
	require.Error(t, database.Create(&duplicate).Error)

	var connections []db.GitHubConnection
	require.NoError(t, database.Where("installation_id = ?", 42).Find(&connections).Error)
	require.Len(t, connections, 2)
}
