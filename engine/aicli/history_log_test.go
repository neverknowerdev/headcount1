package aicli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/engine/aicli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMessageHistoryReconstructsMessagesAndHonorsCheckpointCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	entries := []struct {
		typeName string
		seq      int
		message  *aicli.Message
	}{
		{typeName: "info", seq: 1},
		{typeName: "message", seq: 2, message: &aicli.Message{Role: "system", Content: "system"}},
		{typeName: "message", seq: 3, message: &aicli.Message{Role: "assistant", ToolCalls: []aicli.ToolCall{{ID: "call-1", Type: "function", Function: aicli.FuncCall{Name: "report_status", Arguments: `{"status":"ready"}`}}}}},
		{typeName: "message", seq: 4, message: &aicli.Message{Role: "tool", ToolCallID: "call-1", Content: "done"}},
		{typeName: "message", seq: 5, message: &aicli.Message{Role: "user", Content: "later attempt"}},
	}
	file, err := os.Create(path)
	require.NoError(t, err)
	for _, item := range entries {
		content := "log"
		if item.message != nil {
			messageJSON, marshalErr := json.Marshal(item.message)
			require.NoError(t, marshalErr)
			content = string(messageJSON)
		}
		line, marshalErr := json.Marshal(map[string]interface{}{"type": item.typeName, "seq": item.seq, "content": content})
		require.NoError(t, marshalErr)
		_, err = file.Write(append(line, '\n'))
		require.NoError(t, err)
	}
	require.NoError(t, file.Close())

	history, cursor, err := aicli.LoadMessageHistoryWithCursor(path, 4)
	require.NoError(t, err)
	assert.Equal(t, int64(4), cursor)
	require.Len(t, history, 3)
	assertMessageEqual(t, aicli.Message{Role: "system", Content: "system"}, history[0])
	require.Len(t, history[1].ToolCalls, 1)
	require.Equal(t, "call-1", history[1].ToolCalls[0].ID)
	require.Equal(t, "report_status", history[1].ToolCalls[0].Function.Name)
	assertMessageEqual(t, aicli.Message{Role: "tool", ToolCallID: "call-1", Content: "done"}, history[2])
}

func TestLoadMessageHistoryRejectsMalformedMessageEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"message","seq":1,"content":"not-json"}`+"\n"), 0644))
	_, err := aicli.LoadMessageHistory(path, 1)
	require.Error(t, err)
}

func TestLoadSafeMessageHistoryAtOrBeforeChoosesNearestCompleteToolTurn(t *testing.T) {
	path := writeHistoryLog(t,
		aicli.Message{Role: "system", Content: "system"},
		aicli.Message{Role: "assistant", Content: "before"},
		aicli.Message{Role: "assistant", ToolCalls: []aicli.ToolCall{
			{ID: "a", Type: "function", Function: aicli.FuncCall{Name: "write"}},
			{ID: "b", Type: "function", Function: aicli.FuncCall{Name: "run"}},
		}},
		aicli.Message{Role: "tool", ToolCallID: "a", Content: "ok"},
		aicli.Message{Role: "tool", ToolCallID: "b", Content: "ok"},
		aicli.Message{Role: "user", Content: "after"},
	)
	tests := []struct {
		name string
		id   int64
		want int64
		len  int
	}{
		{name: "exact safe message", id: 2, want: 2, len: 2},
		{name: "inside tool batch", id: 3, want: 2, len: 2},
		{name: "inside tool batch after first result", id: 4, want: 2, len: 2},
		{name: "after all tools", id: 5, want: 5, len: 5},
		{name: "beyond log", id: 99, want: 6, len: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			history, cursor, err := aicli.LoadSafeMessageHistoryAtOrBefore(path, tt.id)
			require.NoError(t, err)
			assert.Equal(t, tt.want, cursor)
			assert.Len(t, history, tt.len)
		})
	}
}

func TestLoadSafeMessageHistoryAtOrBeforeRejectsUnsafeOrInvalidBoundaries(t *testing.T) {
	tests := []struct {
		name string
		msgs []aicli.Message
		id   int64
	}{
		{name: "non-positive ID", msgs: []aicli.Message{{Role: "user", Content: "x"}}, id: 0},
		{name: "before first message", msgs: []aicli.Message{{Role: "user", Content: "x"}}, id: 0},
		{name: "no completed tool result", msgs: []aicli.Message{{Role: "assistant", ToolCalls: []aicli.ToolCall{{ID: "call"}}}}, id: 1},
		{name: "unmatched tool result", msgs: []aicli.Message{{Role: "tool", ToolCallID: "missing"}}, id: 1},
		{name: "missing tool call ID", msgs: []aicli.Message{{Role: "assistant", ToolCalls: []aicli.ToolCall{{}}}}, id: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeHistoryLog(t, tt.msgs...)
			_, _, err := aicli.LoadSafeMessageHistoryAtOrBefore(path, tt.id)
			assert.Error(t, err)
		})
	}
}

func writeHistoryLog(t *testing.T, messages ...aicli.Message) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.jsonl")
	file, err := os.Create(path)
	require.NoError(t, err)
	for i, message := range messages {
		messageJSON, marshalErr := json.Marshal(message)
		require.NoError(t, marshalErr)
		line, marshalErr := json.Marshal(map[string]interface{}{"type": "message", "seq": i + 1, "content": string(messageJSON)})
		require.NoError(t, marshalErr)
		_, err = file.Write(append(line, '\n'))
		require.NoError(t, err)
	}
	require.NoError(t, file.Close())
	return path
}

func assertMessageEqual(t *testing.T, expected, actual aicli.Message) {
	t.Helper()
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	actualJSON, err := json.Marshal(actual)
	require.NoError(t, err)
	require.JSONEq(t, string(expectedJSON), string(actualJSON))
}
