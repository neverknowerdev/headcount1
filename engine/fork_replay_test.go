package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"agent-orchestrator/engine/aicli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplayForkHistoryReplaysStatefulToolsButDoesNotDuplicateControlPlane(t *testing.T) {
	registry := aicli.NewRegistry()
	called := []string{}
	registry.Register(replayEngineTestTool{name: string(aicli.ToolWrite), call: func() { called = append(called, "write") }})
	registry.Register(replayEngineTestTool{name: string(aicli.ToolReportStatus), call: func() { called = append(called, "report_status") }})
	history := []aicli.Message{
		{Role: "assistant", ToolCalls: []aicli.ToolCall{
			{ID: "write-1", Function: aicli.FuncCall{Name: string(aicli.ToolWrite), Arguments: `{}`}},
			{ID: "status-1", Function: aicli.FuncCall{Name: string(aicli.ToolReportStatus), Arguments: `{}`}},
		}},
		{Role: "tool", ToolCallID: "write-1", Content: "written"},
		{Role: "tool", ToolCallID: "status-1", Content: "reported"},
	}

	require.NoError(t, replayForkHistory(context.Background(), registry, history))
	assert.Equal(t, []string{"write"}, called)
}

func TestReplayForkHistoryReturnsStatefulToolErrorToCaller(t *testing.T) {
	want := errors.New("external resource unavailable")
	registry := aicli.NewRegistry()
	registry.Register(replayEngineTestTool{name: string(aicli.ToolBash), err: want})
	history := []aicli.Message{
		{Role: "assistant", ToolCalls: []aicli.ToolCall{
			{ID: "bash-1", Function: aicli.FuncCall{Name: string(aicli.ToolBash), Arguments: `{}`}},
		}},
		{Role: "tool", ToolCallID: "bash-1", Content: "old output"},
	}

	err := replayForkHistory(context.Background(), registry, history)
	var replayErr *aicli.ToolReplayError
	require.ErrorAs(t, err, &replayErr)
	assert.ErrorIs(t, err, want)
	assert.Contains(t, err.Error(), "bash-1")
}

type replayEngineTestTool struct {
	name string
	call func()
	err  error
}

func (t replayEngineTestTool) Def() aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{Name: t.name}}
}

func (t replayEngineTestTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	if t.call != nil {
		t.call()
	}
	return "ok", t.err
}
