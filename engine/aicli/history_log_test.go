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

func assertMessageEqual(t *testing.T, expected, actual aicli.Message) {
	t.Helper()
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	actualJSON, err := json.Marshal(actual)
	require.NoError(t, err)
	require.JSONEq(t, string(expectedJSON), string(actualJSON))
}
