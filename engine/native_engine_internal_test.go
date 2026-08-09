package engine

import (
	"context"
	"encoding/json"
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

func TestApplyStoredToolPermissionsUsesRuntimeAliases(t *testing.T) {
	registry := aicli.NewRegistry()
	for _, name := range []string{"bash", "read", "write", "ls", "grep", "web_fetch", "finish_task"} {
		registry.Register(&permissionTestTool{name: name})
	}

	filtered, err := applyStoredToolPermissions(registry, `{"edit":"deny","bash":"deny"}`)
	require.NoError(t, err)
	require.NotContains(t, filtered.Names(), "write", "UI edit must deny the native write tool")
	require.NotContains(t, filtered.Names(), "bash")
	require.Contains(t, filtered.Names(), "read")
	require.Contains(t, filtered.Names(), "finish_task", "lifecycle tools are not controlled by the UI checklist")

}

func TestTryGitCommitSkipsCleanAndCommitsUntrackedChanges(t *testing.T) {
	workspace := t.TempDir()
	manager := git.NewGitManager(workspace, "")
	require.NoError(t, manager.Init(context.Background()))
	engine := &NativeEngine{}

	require.False(t, engine.tryGitCommit(context.Background(), nil, manager, workspace, db.Task{}, db.Agent{}))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("new\n"), 0644))
	require.True(t, engine.tryGitCommit(context.Background(), nil, manager, workspace, db.Task{}, db.Agent{}))
	status, err := manager.GetStatusInDir(context.Background(), workspace)
	require.NoError(t, err)
	require.Empty(t, status, "a successful commit should leave no worktree changes")
}

type permissionTestTool struct{ name string }

func (t *permissionTestTool) Def() aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
		Name: t.name, Description: t.name, Parameters: []byte(`{"type":"object"}`),
	}}
}

func (t *permissionTestTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", nil
}
