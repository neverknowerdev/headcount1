package db_test

import (
	"context"
	"testing"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMigrateGitHubOAuthDropsLegacyTokenAndDisablesDuplicateIdentity(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.MCPServer{}, &db.MCPAccount{}, &db.GitHubConnection{}, &db.GitHubIdentity{}))
	// Simulate the pre-OAuth schema. The plaintext column must not survive a
	// successful migration, even if legacy rows are present.
	connectionTable := database.NamingStrategy.TableName("GitHubConnection")
	require.NoError(t, database.Exec("ALTER TABLE "+connectionTable+" ADD COLUMN user_access_token text").Error)
	require.NoError(t, database.Exec("INSERT INTO "+connectionTable+" (installation_id, user_access_token) VALUES (?, ?)", 1, "plaintext-token").Error)

	user := db.User{Email: "github-migration@example.test"}
	require.NoError(t, database.Create(&user).Error)
	server := db.MCPServer{Name: "github", AuthType: "github-app"}
	require.NoError(t, database.Create(&server).Error)
	canonical := db.MCPAccount{MCPServerID: server.ID, Name: "canonical", UserID: &user.ID, GitHubUserID: 42, GitHubLogin: "octocat"}
	duplicate := db.MCPAccount{MCPServerID: server.ID, Name: "duplicate", UserID: &user.ID, GitHubUserID: 42, GitHubLogin: "octocat"}
	legacy := db.MCPAccount{MCPServerID: server.ID, Name: "legacy", UserID: &user.ID}
	require.NoError(t, database.Create(&canonical).Error)
	require.NoError(t, database.Create(&duplicate).Error)
	require.NoError(t, database.Create(&legacy).Error)
	accountTable := database.NamingStrategy.TableName("MCPAccount")
	for _, account := range []db.MCPAccount{canonical, duplicate, legacy} {
		require.NoError(t, database.Exec("UPDATE "+accountTable+" SET auth_token = ? WHERE id = ?", "sealed-legacy-token", account.ID).Error)
	}

	require.NoError(t, db.New(database).MigrateGitHubOAuth(context.Background()))
	require.Error(t, database.Exec("SELECT user_access_token FROM "+connectionTable).Error, "legacy plaintext column must be dropped")

	var identity db.GitHubIdentity
	require.NoError(t, database.Where("mcp_account_id = ?", canonical.ID).First(&identity).Error)
	require.Equal(t, int64(42), identity.GitHubUserID)
	var gotDuplicate, gotLegacy db.MCPAccount
	require.NoError(t, database.First(&gotDuplicate, duplicate.ID).Error)
	require.NoError(t, database.First(&gotLegacy, legacy.ID).Error)
	for _, account := range []db.MCPAccount{gotDuplicate, gotLegacy} {
		require.Zero(t, account.GitHubUserID)
		require.Empty(t, account.GitHubLogin)
		require.Empty(t, account.AuthTokenEncrypted)
		require.NotEmpty(t, account.LastError)
	}
}
