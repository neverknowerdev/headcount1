package endpoints

import (
	"encoding/json"
	"testing"

	"agent-orchestrator/db"

	"github.com/stretchr/testify/require"
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
