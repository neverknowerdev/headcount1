package engine

import (
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli/tools"

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

func TestModelGroupGatewayHeadersReuseTheProvidedRunToken(t *testing.T) {
	headers := modelGroupGatewayHeaders(42, "rt_parent")
	require.Equal(t, "rt_parent", headers["X-Gateway-Token"])
	require.Equal(t, "42", headers["X-Run-ID"])
	require.Equal(t, "switches-only", headers["X-Proxy-Log-Mode"])
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
