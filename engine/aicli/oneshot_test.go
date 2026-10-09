package aicli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"agent-orchestrator/engine/aicli"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oneShotServer scripts a provider: each request consumes the next reply and
// the decoded request bodies are kept for assertions.
type oneShotServer struct {
	mu       sync.Mutex
	replies  []func(w http.ResponseWriter)
	requests []aicli.ChatRequest
}

func (s *oneShotServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req aicli.ChatRequest
		require.NoError(t, json.Unmarshal(body, &req))
		s.mu.Lock()
		s.requests = append(s.requests, req)
		index := len(s.requests) - 1
		s.mu.Unlock()
		if index >= len(s.replies) {
			t.Errorf("unexpected provider request #%d", index+1)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		s.replies[index](w)
	}
}

func replyToolCalls(calls ...[2]string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		toolCalls := make([]map[string]any, 0, len(calls))
		for i, call := range calls {
			toolCalls = append(toolCalls, map[string]any{
				"id": fmt.Sprintf("call-%d", i), "type": "function",
				"function": map[string]any{"name": call[0], "arguments": call[1]},
			})
		}
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "test-model",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "tool_calls": toolCalls}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}
}

func replyText(text string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "test-model",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": text}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}
}

func replyBadRequest(message string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error"}})
	}
}

func oneShotRequest() aicli.ChatRequest {
	tool := func(name string) aicli.ToolDef {
		return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{Name: name, Parameters: json.RawMessage(`{"type":"object"}`)}}
	}
	return aicli.ChatRequest{
		Messages: []aicli.Message{{Role: "system", Content: "role"}, {Role: "user", Content: "question"}},
		Tools:    []aicli.ToolDef{tool("finish_phase"), tool("ask_questions")},
	}
}

func newOneShot(t *testing.T, replies ...func(http.ResponseWriter)) (*aicli.Client, *oneShotServer) {
	script := &oneShotServer{replies: replies}
	srv := httptest.NewServer(script.handler(t))
	t.Cleanup(srv.Close)
	return newRetryTestClient(srv), script
}

func TestCompleteWithTool_ReturnsTheSingleCall(t *testing.T) {
	client, script := newOneShot(t, replyToolCalls([2]string{"finish_phase", `{"ok":true}`}))

	result, err := client.CompleteWithTool(context.Background(), oneShotRequest(), nil)
	require.NoError(t, err)
	assert.Equal(t, "finish_phase", result.Call.Function.Name)
	assert.Empty(t, result.Ignored)
	require.Len(t, result.Attempts, 1)
	assert.Equal(t, 10, result.Attempts[0].Response.Usage.PromptTokens)
	assert.Equal(t, aicli.ToolChoiceRequired, script.requests[0].ToolChoice)
}

func TestCompleteWithTool_AppliesFirstCallAndReportsTheRest(t *testing.T) {
	client, _ := newOneShot(t, replyToolCalls([2]string{"ask_questions", `{}`}, [2]string{"finish_phase", `{}`}))

	result, err := client.CompleteWithTool(context.Background(), oneShotRequest(), nil)
	require.NoError(t, err)
	assert.Equal(t, "ask_questions", result.Call.Function.Name)
	require.Len(t, result.Ignored, 1)
	assert.Equal(t, "finish_phase", result.Ignored[0].Function.Name)
}

func TestCompleteWithTool_CorrectsAReplyWithoutAToolCall(t *testing.T) {
	client, script := newOneShot(t, replyText("I think we should..."), replyToolCalls([2]string{"finish_phase", `{}`}))

	result, err := client.CompleteWithTool(context.Background(), oneShotRequest(), nil)
	require.NoError(t, err)
	assert.Equal(t, "finish_phase", result.Call.Function.Name)
	require.Len(t, result.Attempts, 2)

	// The retry is the same prompt plus one corrective line — the rejected
	// reply itself is not carried forward as history.
	retry := script.requests[1].Messages
	require.Len(t, retry, 3)
	assert.Equal(t, "question", retry[1].Content)
	assert.Equal(t, "user", retry[2].Role)
	assert.Contains(t, retry[2].Content, "no tool call")
	assert.Contains(t, retry[2].Content, "finish_phase, ask_questions")
}

func TestCompleteWithTool_CorrectsUnknownToolAndInvalidArguments(t *testing.T) {
	client, script := newOneShot(t,
		replyToolCalls([2]string{"read_file", `{}`}),
		replyToolCalls([2]string{"finish_phase", `{"spec":""}`}),
		replyToolCalls([2]string{"finish_phase", `{"spec":"done"}`}),
	)
	validate := func(call aicli.ToolCall) error {
		var args struct{ Spec string }
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return err
		}
		if args.Spec == "" {
			return errors.New("spec is required")
		}
		return nil
	}

	result, err := client.CompleteWithTool(context.Background(), oneShotRequest(), validate)
	require.NoError(t, err)
	assert.JSONEq(t, `{"spec":"done"}`, result.Call.Function.Arguments)
	require.Len(t, result.Attempts, 3)
	assert.Contains(t, script.requests[1].Messages[2].Content, `tool "read_file" does not exist`)
	assert.Contains(t, script.requests[2].Messages[2].Content, "spec is required")
	// Corrections do not accumulate: each retry carries only the latest one.
	assert.Len(t, script.requests[2].Messages, 3)
}

func TestCompleteWithTool_GivesUpAfterBoundedCorrections(t *testing.T) {
	client, script := newOneShot(t, replyText("a"), replyText("b"), replyText("c"))

	result, err := client.CompleteWithTool(context.Background(), oneShotRequest(), nil)
	require.ErrorIs(t, err, aicli.ErrNoToolCall)
	require.NotNil(t, result)
	assert.Len(t, result.Attempts, 3, "every paid attempt is reported alongside the error")
	assert.Len(t, script.requests, 3)
}

func TestCompleteWithTool_DowngradesWhenProviderRejectsToolChoice(t *testing.T) {
	client, script := newOneShot(t,
		replyBadRequest("Unsupported parameter: tool_choice"),
		replyToolCalls([2]string{"finish_phase", `{}`}),
		replyToolCalls([2]string{"finish_phase", `{}`}),
	)

	result, err := client.CompleteWithTool(context.Background(), oneShotRequest(), nil)
	require.NoError(t, err)
	require.Len(t, result.Attempts, 2)
	assert.Error(t, result.Attempts[0].Err)
	assert.Equal(t, aicli.ToolChoiceRequired, script.requests[0].ToolChoice)
	assert.Empty(t, script.requests[1].ToolChoice)
	assert.Len(t, script.requests[1].Messages, 2, "the downgrade retries the unchanged prompt")

	// The rejection is remembered for this provider and model.
	_, err = client.CompleteWithTool(context.Background(), oneShotRequest(), nil)
	require.NoError(t, err)
	assert.Empty(t, script.requests[2].ToolChoice)
}

func TestCompleteWithTool_SurfacesHardProviderErrors(t *testing.T) {
	client, script := newOneShot(t, replyBadRequest("model not found"))

	result, err := client.CompleteWithTool(context.Background(), oneShotRequest(), nil)
	require.Error(t, err)
	assert.False(t, errors.Is(err, aicli.ErrNoToolCall))
	assert.True(t, strings.Contains(err.Error(), "model not found"))
	assert.Len(t, result.Attempts, 1)
	assert.Len(t, script.requests, 1)
}

func TestCompleteWithTool_RequiresTools(t *testing.T) {
	client, _ := newOneShot(t)
	_, err := client.CompleteWithTool(context.Background(), aicli.ChatRequest{}, nil)
	require.Error(t, err)
}
