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
