package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/git"

	"github.com/stretchr/testify/require"
)

func TestPullRequestContentUsesAgentProvidedMetadata(t *testing.T) {
	task := db.Task{Title: "Fallback title", Description: "Fallback description"}
	finish := tools.FinishTaskResult{
		FinishStatus:           "Implemented OAuth",
		ResultDetails:          "Detailed handoff",
		PullRequestTitle:       "Improve GitHub OAuth",
		PullRequestDescription: "Adds account isolation and tests.",
	}
	title, description := pullRequestContent(task, finish)
	require.Equal(t, "Improve GitHub OAuth", title)
	require.Equal(t, "Adds account isolation and tests.", description)
}

func TestPullRequestContentFallsBackToFinishHandoff(t *testing.T) {
	task := db.Task{Title: "Task title", Description: "Task description"}
	title, description := pullRequestContent(task, tools.FinishTaskResult{ResultDetails: "Implemented and verified."})
	require.Equal(t, "Task title", title)
	require.Equal(t, "Implemented and verified.", description)
}

func TestIsModelGroupProxyBaseURL(t *testing.T) {
	require.True(t, isModelGroupProxyBaseURL("http://127.0.0.1:8080/api/proxy/group/free-first"))
	require.True(t, isModelGroupProxyBaseURL("http://test.local/api/proxy/group/free-first/v1"))
	require.False(t, isModelGroupProxyBaseURL("https://api.openai.com/v1"))
}

func TestTryGitCommitSkipsCleanAndCommitsUntrackedChanges(t *testing.T) {
	workspace := t.TempDir()
	manager := git.NewGitManager(workspace, "")
	require.NoError(t, manager.Init(context.Background()))
	engine := &NativeEngine{}

	require.False(t, engine.tryGitCommit(context.Background(), nil, manager, workspace, db.Task{}, db.Agent{}, runGatewayAuth{}))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("new\n"), 0644))
	require.True(t, engine.tryGitCommit(context.Background(), nil, manager, workspace, db.Task{}, db.Agent{}, runGatewayAuth{}))
	status, err := manager.GetStatusInDir(context.Background(), workspace)
	require.NoError(t, err)
	require.Empty(t, status, "a successful commit should leave no worktree changes")
}

func TestRunGatewayAuthReusesTheProvidedRunToken(t *testing.T) {
	client := aicli.NewClient("http://localhost/api/proxy/group/free-first", "", "model")
	auth := runGatewayAuth{runID: 42, token: "rt_parent"}
	require.NoError(t, auth.configure(client, db.LLMProvider{BaseUrl: client.BaseURL}))
	headers := client.ExtraHeaders
	require.Equal(t, "rt_parent", headers["X-Gateway-Token"])
	require.Equal(t, "42", headers["X-Run-ID"])
	require.Equal(t, "switches-only", headers["X-Proxy-Log-Mode"])
}

func TestRunGatewayAuthRejectsMissingTokenForProxyProvider(t *testing.T) {
	client := aicli.NewClient("http://localhost/api/proxy/group/free-first", "", "model")
	auth := runGatewayAuth{runID: 42}
	require.ErrorContains(t, auth.configure(client, db.LLMProvider{BaseUrl: client.BaseURL}), "gateway token is unavailable")
}

func TestFinishAllowsGitOnlyForSuccessfulVerdicts(t *testing.T) {
	require.True(t, finishAllowsGit(tools.FinishTaskResult{Status: "done"}))
	require.True(t, finishAllowsGit(tools.FinishTaskResult{Status: "in-review"}))
	require.False(t, finishAllowsGit(tools.FinishTaskResult{Status: "blocked"}))
	require.False(t, finishAllowsGit(tools.FinishTaskResult{Status: "refinement"}))
	require.False(t, finishAllowsGit(tools.FinishTaskResult{}))
}

func TestCommitRelevantGitStatusIgnoresTaskMemory(t *testing.T) {
	status := "?? memory.md\n M frontend/src/App.tsx\n"
	require.Equal(t, " M frontend/src/App.tsx", commitRelevantGitStatus(status))
	require.Empty(t, commitRelevantGitStatus("?? memory.md\n"))
}
