package tools

import (
	"context"
	"testing"

	"agent-orchestrator/engine/aicli"

	"github.com/stretchr/testify/require"
)

func TestWorkerRegistryIsIndependentAndExact(t *testing.T) {
	r := NewWorkerRegistry(t.TempDir(), nil, WorkerCallbacks{
		ReportStatus: func(context.Context, string, int64) error { return nil },
		FinishWork:   func(context.Context, FinishWorkResult) (string, error) { return "ok", nil },
	})
	require.Equal(t, []string{
		string(aicli.ToolBash), string(aicli.ToolBrowserUse), string(aicli.ToolFinishWork),
		string(aicli.ToolGrep), string(aicli.ToolListDir), string(aicli.ToolRead),
		string(aicli.ToolReportStatus), string(aicli.ToolWebFetch), string(aicli.ToolWrite),
	}, r.Names())
	for _, forbidden := range []aicli.ToolName{
		aicli.ToolFinishTask, aicli.ToolAskTaskOwner, aicli.ToolCreateTask,
		aicli.ToolCreateSubtask, aicli.ToolWriteArtifact, aicli.ToolRunWorker,
		aicli.ToolAnswerMessage,
	} {
		require.NotContains(t, r.Names(), string(forbidden))
	}
}

func TestFinishWorkValidatesStructuredTerminalContract(t *testing.T) {
	called := false
	tool := NewFinishWork(func(_ context.Context, result FinishWorkResult) (string, error) {
		called = true
		require.Equal(t, "done", result.Status)
		return "recorded", nil
	})
	_, err := tool.Execute(context.Background(), []byte(`{"status":"done","summary":"evidence collected"}`))
	require.NoError(t, err)
	require.True(t, called)
	_, err = tool.Execute(context.Background(), []byte(`{"status":"done","summary":""}`))
	require.Error(t, err)
}
