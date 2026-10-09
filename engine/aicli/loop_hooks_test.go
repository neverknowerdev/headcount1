package aicli_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"agent-orchestrator/engine/aicli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type queuedCompletionTransport struct {
	responses [][]byte
	index     atomic.Int32
}

func (t *queuedCompletionTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	i := int(t.index.Add(1)) - 1
	if i >= len(t.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(string(t.responses[i]))),
		Header:     http.Header{"Content-Type": {"application/json"}},
	}, nil
}

func completionBody(t *testing.T, content string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": content}}},
	})
	require.NoError(t, err)
	return body
}

// A session that ends its turn with plain text can be sent back to work: the
// hook's messages are appended and the loop asks the model again.
func TestAgentOnFinalTextAppendsMessagesAndContinues(t *testing.T) {
	transport := &queuedCompletionTransport{responses: [][]byte{
		completionBody(t, "main response before the reminder"),
		completionBody(t, "continued main response"),
	}}
	client := aicli.NewClient("http://unused", "", "test-model")
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: transport}
	var reminded atomic.Bool
	var seenHistory []aicli.Message
	agent := aicli.New(aicli.Config{
		Client: client, Registry: aicli.NewRegistry(),
		OnFinalText: func(_ context.Context, history []aicli.Message) ([]aicli.Message, error) {
			if reminded.Swap(true) {
				return nil, nil
			}
			seenHistory = append([]aicli.Message(nil), history...)
			return []aicli.Message{{Role: "user", Content: "You have not reported yet."}}, nil
		},
	})

	result, history, err := agent.RunWithHistory(context.Background(), []aicli.Message{{Role: "user", Content: "main task"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "continued main response", result)
	assert.Equal(t, int32(2), transport.index.Load())
	require.Len(t, seenHistory, 2)
	assert.Equal(t, "main task", seenHistory[0].Content)
	assert.Equal(t, "main response before the reminder", seenHistory[1].Content)
	assert.Equal(t, []aicli.Message{
		{Role: "user", Content: "main task"},
		{Role: "assistant", Content: "main response before the reminder"},
		{Role: "user", Content: "You have not reported yet."},
		{Role: "assistant", Content: "continued main response"},
	}, history)
}

func TestAgentBeforeTurnMessagesFollowToolResults(t *testing.T) {
	transport := &queuedCompletionTransport{responses: [][]byte{
		toolCallBody(t, "call-1", "test_tool"),
		completionBody(t, "main response after tool"),
	}}
	client := aicli.NewClient("http://unused", "", "test-model")
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: transport}
	reg := aicli.NewRegistry()
	reg.Register(testInterruptTool{})
	var beforeTurnCalls atomic.Int32
	var beforeTurnHistory []aicli.Message
	agent := aicli.New(aicli.Config{
		Client: client, Registry: reg,
		BeforeTurn: func(_ context.Context, history []aicli.Message) ([]aicli.Message, error) {
			if beforeTurnCalls.Add(1) != 2 {
				return nil, nil
			}
			beforeTurnHistory = append([]aicli.Message(nil), history...)
			return []aicli.Message{{Role: "user", Content: "question after tool"}, {Role: "assistant", Content: "answer after tool"}}, nil
		},
	})
	result, history, err := agent.RunWithHistory(context.Background(), []aicli.Message{{Role: "user", Content: "main task"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "main response after tool", result)
	// The control pair appears after the tool result, never between the
	// assistant tool-call message and its required tool response.
	assert.Equal(t, "tool", history[2].Role)
	assert.Equal(t, "question after tool", history[3].Content)
	require.Len(t, beforeTurnHistory, 3)
	assert.Equal(t, "tool", beforeTurnHistory[2].Role)
	assert.Equal(t, "tool result", beforeTurnHistory[2].Content)
}

// A tool that fails does not end the session: the model sees the error as the
// tool's result and decides what to do about it.
func TestToolErrorBecomesToolResult(t *testing.T) {
	transport := &queuedCompletionTransport{responses: [][]byte{
		toolCallWithArgumentsBody(t, "call-1", "failing_tool", `{}`),
		completionBody(t, "I saw the tool error and can continue."),
	}}
	client := aicli.NewClient("http://unused", "", "test-model")
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: transport}
	registry := aicli.NewRegistry()
	registry.Register(failingTool{})
	agent := aicli.New(aicli.Config{Client: client, Registry: registry})
	result, history, err := agent.RunWithHistory(context.Background(), []aicli.Message{{Role: "user", Content: "do the task"}}, nil)
	require.NoError(t, err)
	assert.Contains(t, result, "tool error")
	var toolResult string
	for _, message := range history {
		if message.Role == "tool" {
			toolResult = message.Content
		}
	}
	assert.Contains(t, toolResult, "context deadline exceeded")
}

type failingTool struct{}

func (failingTool) Def() aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{Name: "failing_tool", Parameters: json.RawMessage(`{"type":"object"}`)}}
}

func (failingTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", context.DeadlineExceeded
}

type testInterruptTool struct{}

func (testInterruptTool) Def() aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{Name: "test_tool", Parameters: json.RawMessage(`{"type":"object"}`)}}
}

func (testInterruptTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "tool result", nil
}

func toolCallBody(t *testing.T, id, name string) []byte {
	return toolCallWithArgumentsBody(t, id, name, "{}")
}

func toolCallWithArgumentsBody(t *testing.T, id, name, arguments string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{{"message": map[string]interface{}{
			"role": "assistant", "tool_calls": []map[string]interface{}{{"id": id, "type": "function", "function": map[string]string{"name": name, "arguments": arguments}}},
		}}},
	})
	require.NoError(t, err)
	return body
}
