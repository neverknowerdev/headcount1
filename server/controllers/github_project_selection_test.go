package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/githubapp"
	"agent-orchestrator/pkg/secrets"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResolveGitHubRepositoryRejectsMalformedSelection(t *testing.T) {
	api := &API{}
	_, _, err := api.resolveGitHubRepository(t.Context(), 1, json.RawMessage(`{"id":1}`))
	require.EqualError(t, err, "invalid GitHub repository selection")
}

func TestClearGitHubRepositoryMetadataForManualRepository(t *testing.T) {
	project := db.Project{GitHubRepositoryID: 42, GitHubInstallationID: 77, GitHubDefaultBranch: "main"}
	clearGitHubRepositoryMetadata(&project)
	require.Zero(t, project.GitHubRepositoryID)
	require.Zero(t, project.GitHubInstallationID)
	require.Empty(t, project.GitHubDefaultBranch)
}

func TestGitHubOAuthStateIsBoundToTheUserWhoStartedIt(t *testing.T) {
	state := db.GitHubOAuthState{UserID: 101}
	require.True(t, githubOAuthStateBelongsToUser(state, 101))
	// A callback URL copied into another signed-in Headcount1 session must be
	// rejected before state is marked used or GitHub's code is exchanged.
	require.False(t, githubOAuthStateBelongsToUser(state, 202))
	require.False(t, githubOAuthStateBelongsToUser(state, 0))
}

func TestAuthenticatedGitHubEndpointsRejectMissingUserContext(t *testing.T) {
	api := &API{}
	for name, endpoint := range map[string]struct {
		handler http.HandlerFunc
		target  string
	}{
		"status":       {api.GitHubStatus, "/github/status"},
		"callback":     {api.GitHubCallback, "/github/callback?state=x&code=y"},
		"repositories": {api.ListGitHubRepositories, "/github/repositories"},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, endpoint.target, nil)
			response := httptest.NewRecorder()
			endpoint.handler(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code)
		})
	}
}

func TestGitHubCallbackDoesNotConsumeAnotherUsersState(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.GitHubOAuthState{}))
	state := db.GitHubOAuthState{ID: "state", UserID: 10, MCPServerID: 1, ExpiresAt: time.Now().Add(time.Minute)}
	require.NoError(t, database.Create(&state).Error)
	request := httptest.NewRequest(http.MethodGet, "/github/callback?state=state&code=code", nil)
	request = request.WithContext(authctx.WithUser(request.Context(), db.User{ID: 20}))
	response := httptest.NewRecorder()
	NewAPI(database, nil, nil).GitHubCallback(response, request)
	require.Equal(t, http.StatusForbidden, response.Code)
	var stored db.GitHubOAuthState
	require.NoError(t, database.First(&stored, "id = ?", state.ID).Error)
	require.Nil(t, stored.UsedAt)
}

func TestGitHubCallbackRejectsStateForNonBuiltinGitHubServerBeforeClaim(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.MCPServer{}, &db.GitHubOAuthState{}))
	user := db.User{ID: 10}
	lookalike := db.MCPServer{Name: "github-lookalike", AuthType: db.MCPAuthTypeGitHubApp, Builtin: false}
	require.NoError(t, database.Create(&lookalike).Error)
	state := db.GitHubOAuthState{ID: "state", UserID: user.ID, MCPServerID: lookalike.ID, ExpiresAt: time.Now().Add(time.Minute)}
	require.NoError(t, database.Create(&state).Error)
	request := httptest.NewRequest(http.MethodGet, "/github/callback?state=state&code=code", nil)
	request = request.WithContext(authctx.WithUser(request.Context(), user))
	response := httptest.NewRecorder()
	NewAPI(database, nil, nil).GitHubCallback(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	var stored db.GitHubOAuthState
	require.NoError(t, database.First(&stored, "id = ?", state.ID).Error)
	require.Nil(t, stored.UsedAt)
}

func TestGitHubOAuthAccountUsesAuthenticatedGitHubLoginAsName(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.MCPServer{}, &db.MCPAccount{}, &db.GitHubIdentity{}, &db.GitHubConnection{}))
	user := db.User{Email: "user@example.test"}
	server := db.MCPServer{Name: db.MCPServerNameGitHub, AuthType: db.MCPAuthTypeGitHubApp, Builtin: true}
	require.NoError(t, database.Create(&user).Error)
	require.NoError(t, database.Create(&server).Error)
	dek, err := secrets.NewUserDEK()
	require.NoError(t, err)
	secrets.Default().UnlockUser(user.ID, dek, time.Minute)
	defer secrets.Default().LockUser(user.ID)
	firstToken, err := secrets.Default().EncryptForUser(user.ID, "token")
	require.NoError(t, err)
	secondToken, err := secrets.Default().EncryptForUser(user.ID, "new-token")
	require.NoError(t, err)

	api := NewAPI(database, nil, nil)
	state := db.GitHubOAuthState{MCPServerID: server.ID, UserID: user.ID}
	account, err := api.persistGitHubOAuthAccount(context.Background(), state, githubapp.User{ID: 42, Login: "octocat"}, firstToken, nil, time.Now())
	require.NoError(t, err)
	require.Equal(t, "octocat", account.Name)

	state.MCPAccountID = account.ID
	account, err = api.persistGitHubOAuthAccount(context.Background(), state, githubapp.User{ID: 42, Login: "octocat-renamed"}, secondToken, nil, time.Now())
	require.NoError(t, err)
	require.Equal(t, "octocat-renamed", account.Name)
}

func TestGitHubAccountChooserIsOnlyUsedWhenAddingAnotherAccount(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.MCPAccount{}))
	api := NewAPI(database, nil, nil)

	selectAccount, err := api.shouldSelectGitHubAccount(t.Context(), 7, 11, 0)
	require.NoError(t, err)
	require.False(t, selectAccount, "the first GitHub connection uses the normal OAuth page")

	userID := int32(11)
	require.NoError(t, database.Create(&db.MCPAccount{MCPServerID: 7, Name: "octocat", UserID: &userID}).Error)
	selectAccount, err = api.shouldSelectGitHubAccount(t.Context(), 7, 11, 0)
	require.NoError(t, err)
	require.True(t, selectAccount, "adding another account opens GitHub's account chooser")

	selectAccount, err = api.shouldSelectGitHubAccount(t.Context(), 7, 11, 1)
	require.NoError(t, err)
	require.False(t, selectAccount, "re-authentication does not need an account chooser")
}
