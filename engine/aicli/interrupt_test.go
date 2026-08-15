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
	orchestratorTools "agent-orchestrator/engine/aicli/tools"
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

func TestAgentInterruptAppendsIsolatedQuestionAndAnswerThenContinues(t *testing.T) {
	transport := &queuedCompletionTransport{responses: [][]byte{
		completionBody(t, "main response before interruption"),
		completionBody(t, "continued main response"),
	}}
	client := aicli.NewClient("http://unused", "", "test-model")
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: transport}
	var interrupted atomic.Bool
	var interruptHistory []aicli.Message
	agent := aicli.New(aicli.Config{
		Client: client, Registry: aicli.NewRegistry(),
		Interrupt: func(_ context.Context, history []aicli.Message) ([]aicli.Message, error) {
			if interrupted.Swap(true) {
				return nil, nil
			}
			interruptHistory = append([]aicli.Message(nil), history...)
			return []aicli.Message{
				{Role: "user", Content: "Orchestrator question"},
				{Role: "assistant", Content: "isolated answer"},
			}, nil
		},
	})

	result, history, err := agent.RunWithHistory(context.Background(), []aicli.Message{{Role: "user", Content: "main task"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "continued main response", result)
	assert.Equal(t, int32(2), transport.index.Load())
	require.Len(t, interruptHistory, 2)
	assert.Equal(t, "main task", interruptHistory[0].Content)
	assert.Equal(t, "main response before interruption", interruptHistory[1].Content)
	assert.Equal(t, []aicli.Message{
		{Role: "user", Content: "main task"},
		{Role: "assistant", Content: "main response before interruption"},
		{Role: "user", Content: "Orchestrator question"},
		{Role: "assistant", Content: "isolated answer"},
		{Role: "assistant", Content: "continued main response"},
	}, history)
}

func TestAgentBeforeTurnInterruptionRunsAfterToolResults(t *testing.T) {
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

func TestOrchestratorQuestionErrorBecomesToolResult(t *testing.T) {
	transport := &queuedCompletionTransport{responses: [][]byte{
		toolCallWithArgumentsBody(t, "ask-1", "ask_agent", `{"session_id":7,"question":"status?"}`),
		completionBody(t, "I saw the question error and can continue."),
	}}
	client := aicli.NewClient("http://unused", "", "test-model")
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: transport}
	registry := orchestratorTools.NewOrchestratorRegistry(orchestratorTools.OrchestratorCallbacks{
		GetSessionList: func(context.Context) ([]orchestratorTools.ManagedSessionSummary, error) { return nil, nil },
		GetSession: func(context.Context, int32) (orchestratorTools.ManagedSessionDetails, error) {
			return orchestratorTools.ManagedSessionDetails{}, nil
		},
		AskAgent: func(context.Context, int32, string) (string, error) {
			return "", context.DeadlineExceeded
		},
		RunNewSession: func(context.Context, *int32, string, string) (string, error) { return "", nil },
		StopSession:   func(context.Context, int32, string) (string, error) { return "", nil },
		ForkSession:   func(context.Context, int32, int64) (string, error) { return "", nil },
	})
	agent := aicli.New(aicli.Config{Client: client, Registry: registry})
	result, history, err := agent.RunWithHistory(context.Background(), []aicli.Message{{Role: "user", Content: "manage this task"}}, nil)
	require.NoError(t, err)
	assert.Contains(t, result, "question error")
	var toolResult string
	for _, message := range history {
		if message.Role == "tool" {
			toolResult = message.Content
		}
	}
	assert.Contains(t, toolResult, "context deadline exceeded")
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
