package db_test

import (
	"testing"
	"time"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGitHubQueriesKeepAccountsIsolatedByUserAndBuiltinServer(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.MCPServer{}, &db.MCPAccount{}, &db.GitHubIdentity{}, &db.GitHubConnection{}))
	q := db.New(database)

	owner := db.User{Email: "owner@example.test"}
	other := db.User{Email: "other@example.test"}
	require.NoError(t, database.Create(&owner).Error)
	require.NoError(t, database.Create(&other).Error)
	builtin := db.MCPServer{Name: db.MCPServerNameGitHub, AuthType: db.MCPAuthTypeGitHubApp, Builtin: true}
	lookalike := db.MCPServer{Name: "github-lookalike", AuthType: db.MCPAuthTypeGitHubApp, Builtin: false, OwnerUserID: &owner.ID}
	require.NoError(t, database.Create(&builtin).Error)
	require.NoError(t, database.Create(&lookalike).Error)
	account := db.MCPAccount{MCPServerID: builtin.ID, Name: "owner", UserID: &owner.ID}
	fakeAccount := db.MCPAccount{MCPServerID: lookalike.ID, Name: "fake", UserID: &owner.ID}
	require.NoError(t, database.Create(&account).Error)
	require.NoError(t, database.Create(&fakeAccount).Error)
	require.NoError(t, database.Create(&db.GitHubIdentity{MCPAccountID: account.ID, MCPServerID: builtin.ID, UserID: owner.ID, GitHubUserID: 42, GitHubLogin: "owner"}).Error)

	ownerAccounts, err := q.ListGitHubAccountsForUser(t.Context(), owner.ID)
	require.NoError(t, err)
	require.Len(t, ownerAccounts, 1)
	require.Equal(t, account.ID, ownerAccounts[0].ID)
	otherAccounts, err := q.ListGitHubAccountsForUser(t.Context(), other.ID)
	require.NoError(t, err)
	require.Empty(t, otherAccounts)
	_, err = q.GetGitHubAccountByIDForUser(t.Context(), account.ID, other.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = q.GetGitHubAccountByIDForUser(t.Context(), fakeAccount.ID, owner.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	valid, err := q.HasGitHubIdentity(t.Context(), account, owner.ID)
	require.NoError(t, err)
	require.True(t, valid)
	valid, err = q.HasGitHubIdentity(t.Context(), account, other.ID)
	require.NoError(t, err)
	require.False(t, valid)
}

func TestSaveGitHubOAuthAccountCannotReauthenticateAnotherUsersAccount(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.MCPServer{}, &db.MCPAccount{}, &db.GitHubIdentity{}, &db.GitHubConnection{}))
	q := db.New(database)
	ownerID, attackerID := int32(1), int32(2)
	server := db.MCPServer{Name: db.MCPServerNameGitHub, AuthType: db.MCPAuthTypeGitHubApp, Builtin: true}
	require.NoError(t, database.Create(&server).Error)
	account := db.MCPAccount{MCPServerID: server.ID, Name: "owner", UserID: &ownerID}
	require.NoError(t, database.Create(&account).Error)

	_, err = q.SaveGitHubOAuthAccount(t.Context(), db.SaveGitHubOAuthAccountParams{
		State:        db.GitHubOAuthState{MCPServerID: server.ID, MCPAccountID: account.ID, UserID: attackerID},
		GitHubUserID: 99, GitHubLogin: "attacker", SealedToken: "", ConnectedAt: time.Now(),
	})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var unchanged db.MCPAccount
	require.NoError(t, database.First(&unchanged, account.ID).Error)
	require.Equal(t, "owner", unchanged.Name)
}

func TestSaveGitHubOAuthAccountRejectsDuplicateIdentityForSameUser(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.MCPServer{}, &db.MCPAccount{}, &db.GitHubIdentity{}, &db.GitHubConnection{}))
	q := db.New(database)
	userID := int32(7)
	server := db.MCPServer{Name: db.MCPServerNameGitHub, AuthType: db.MCPAuthTypeGitHubApp, Builtin: true}
	require.NoError(t, database.Create(&server).Error)
	params := db.SaveGitHubOAuthAccountParams{
		State:        db.GitHubOAuthState{MCPServerID: server.ID, UserID: userID},
		GitHubUserID: 42, GitHubLogin: "octocat", ConnectedAt: time.Now(),
	}
	_, err = q.SaveGitHubOAuthAccount(t.Context(), params)
	require.NoError(t, err)
	_, err = q.SaveGitHubOAuthAccount(t.Context(), params)
	require.ErrorIs(t, err, db.ErrGitHubIdentityAlreadyConnected)
	var accounts int64
	require.NoError(t, database.Model(&db.MCPAccount{}).Count(&accounts).Error)
	require.Equal(t, int64(1), accounts)
}

func TestGitHubInstallationLookupIsScopedToTheAccountOwner(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.MCPAccount{}, &db.GitHubConnection{}))
	q := db.New(database)
	ownerID, otherID := int32(1), int32(2)
	account := db.MCPAccount{Name: "owner", UserID: &ownerID}
	require.NoError(t, database.Create(&account).Error)
	connection := db.GitHubConnection{MCPAccountID: account.ID, UserID: ownerID, InstallationID: 123, ConnectedAt: time.Now()}
	require.NoError(t, database.Create(&connection).Error)

	got, err := q.GetGitHubConnectionForAccount(t.Context(), account.ID, ownerID)
	require.NoError(t, err)
	require.Equal(t, int64(123), got.InstallationID)
	_, err = q.GetGitHubConnectionForAccount(t.Context(), account.ID, otherID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
