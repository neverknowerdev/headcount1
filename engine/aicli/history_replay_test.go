package aicli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"agent-orchestrator/engine/aicli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplayCompletedToolCallsPreservesOrderAndRecordedResults(t *testing.T) {
	history := []aicli.Message{
		{Role: "system", Content: "system"},
		{Role: "assistant", ToolCalls: []aicli.ToolCall{
			{ID: "read-1", Type: "function", Function: aicli.FuncCall{Name: "read", Arguments: `{"path":"a.go"}`}},
			{ID: "write-1", Type: "function", Function: aicli.FuncCall{Name: "write", Arguments: `{"path":"b.go","content":"new"}`}},
		}},
		{Role: "tool", ToolCallID: "read-1", Name: "read", Content: "old source"},
		{Role: "tool", ToolCallID: "write-1", Name: "write", Content: "file written"},
		{Role: "assistant", ToolCalls: []aicli.ToolCall{{ID: "bash-1", Type: "function", Function: aicli.FuncCall{Name: "bash", Arguments: `{"command":"go test ./..."}`}}}},
		{Role: "tool", ToolCallID: "bash-1", Name: "bash", Content: "tests passed"},
	}

	var calls []string
	err := aicli.ReplayCompletedToolCalls(context.Background(), history, func(_ context.Context, replay aicli.ToolReplay) error {
		calls = append(calls, fmt.Sprintf("%s:%s", replay.Call.Function.Name, replay.RecordedResult))
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"read:old source", "write:file written", "bash:tests passed"}, calls)
}

func TestReplayCompletedToolCallsReturnsTheFailingCallAndStops(t *testing.T) {
	history := []aicli.Message{
		{Role: "assistant", ToolCalls: []aicli.ToolCall{{ID: "bad-1", Type: "function", Function: aicli.FuncCall{Name: "bash", Arguments: `{"command":"fail"}`}}}},
		{Role: "tool", ToolCallID: "bad-1", Name: "bash", Content: "error: old failure"},
		{Role: "assistant", ToolCalls: []aicli.ToolCall{{ID: "never-1", Type: "function", Function: aicli.FuncCall{Name: "write", Arguments: `{"path":"never","content":"no"}`}}}},
		{Role: "tool", ToolCallID: "never-1", Name: "write", Content: "not reached"},
	}
	want := errors.New("permission denied")
	called := 0
	err := aicli.ReplayCompletedToolCalls(context.Background(), history, func(_ context.Context, replay aicli.ToolReplay) error {
		called++
		assert.Equal(t, "bad-1", replay.Call.ID)
		return want
	})

	var replayErr *aicli.ToolReplayError
	require.ErrorAs(t, err, &replayErr)
	assert.ErrorIs(t, err, want)
	assert.Equal(t, "bash", replayErr.Call.Function.Name)
	assert.Equal(t, "bad-1", replayErr.Call.ID)
	assert.Equal(t, 1, called)
}

func TestReplayCompletedToolCallsRejectsPartialHistory(t *testing.T) {
	history := []aicli.Message{{Role: "assistant", ToolCalls: []aicli.ToolCall{{ID: "pending", Type: "function", Function: aicli.FuncCall{Name: "write"}}}}}
	err := aicli.ReplayCompletedToolCalls(context.Background(), history, func(context.Context, aicli.ToolReplay) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no persisted result")
}

func TestReplayRegistryToolCallExecutesOriginalArguments(t *testing.T) {
	registry := aicli.NewRegistry()
	registry.Register(replayTestTool{name: "write", execute: func(args string) { assert.JSONEq(t, `{"path":"out.txt"}`, args) }})
	err := aicli.ReplayRegistryToolCall(context.Background(), aicli.ToolReplay{
		Call: aicli.ToolCall{ID: "write-1", Function: aicli.FuncCall{Name: "write", Arguments: `{"path":"out.txt"}`}},
	}, registry)
	require.NoError(t, err)
}

type replayTestTool struct {
	name    string
	execute func(string)
}

func (t replayTestTool) Def() aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{Name: t.name}}
}

func (t replayTestTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	t.execute(string(args))
	return "ok", nil
}
