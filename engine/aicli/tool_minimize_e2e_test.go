package aicli_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureServer is a test HTTP server that records every ChatRequest it receives
// and returns canned responses in sequence.
type captureServer struct {
	mu       sync.Mutex
	requests []aicli.ChatRequest
	srv      *httptest.Server
	resps    []string
	idx      int
}

func newCaptureServer(t *testing.T, responses []string) *captureServer {
	t.Helper()
	cs := &captureServer{resps: responses}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req aicli.ChatRequest
		_ = json.Unmarshal(body, &req)

		cs.mu.Lock()
		cs.requests = append(cs.requests, req)
		idx := cs.idx
		cs.idx++
		cs.mu.Unlock()

		if idx >= len(responses) {
			http.Error(w, "fixture exhausted", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, responses[idx])
	}))
	t.Cleanup(cs.srv.Close)
	return cs
}

func (cs *captureServer) Requests() []aicli.ChatRequest {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := make([]aicli.ChatRequest, len(cs.requests))
	copy(out, cs.requests)
	return out
}

func chatResp(content string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"id":    "chatcmpl-test",
		"model": "test-model",
		"choices": []map[string]interface{}{{
			"index": 0,
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	return string(b)
}

func toolCallResp(toolCallID, toolName, arguments string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"id":    "chatcmpl-test",
		"model": "test-model",
		"choices": []map[string]interface{}{{
			"index": 0,
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]interface{}{{
					"id":   toolCallID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      toolName,
						"arguments": arguments,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]interface{}{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30},
	})
	return string(b)
}

// TestAgentToolMinimization_HistoryIsCollapsedAfterMinimize verifies the core
// minimization flow:
//  1. Model calls bash → gets full output
//  2. Model calls minimize_tool_result → result stored compactly
//  3. Model gives final answer → history sent to LLM has minimized content
func TestAgentToolMinimization_HistoryIsCollapsedAfterMinimize(t *testing.T) {
	const bashOutput = "hello world\n"
	const minimizeSummary = "Echo returned greeting"
	const minimizeArgs = `{"tool_call_id":"call_bash_1","summary":"Echo returned greeting","key_findings":["Output: hello world"]}`

	cs := newCaptureServer(t, []string{
		// Turn 1: model calls bash
		toolCallResp("call_bash_1", "bash", `{"command":"echo hello world"}`),
		// Turn 2: model calls minimize_tool_result
		toolCallResp("call_min_1", "minimize_tool_result", minimizeArgs),
		// Turn 3: model gives final answer
		chatResp("Done. The output was: hello world"),
	})

	workDir := t.TempDir()
	reg := tools.DefaultRegistry(workDir)

	store := aicli.NewToolResultStore()
	reg.Register(tools.NewMinimizeToolResult(store.Minimize))
	reg.Register(tools.NewExpandToolResult(store.Expand))

	client := aicli.NewClient(cs.srv.URL, "", "test-model")
	client.MaxRetries = 0

	agent := aicli.New(aicli.Config{
		Client:   client,
		Registry: reg,
		Store:    store,
	})

	result, err := agent.Run(context.Background(), "", "Run echo hello world and tell me the output.")
	require.NoError(t, err)
	assert.Contains(t, result, "hello world")

	reqs := cs.Requests()
	require.Len(t, reqs, 3, "expected exactly 3 LLM calls")

	// Turn 1 (index 0): bash tool result is NOT yet minimized — full content.
	// The history at this point only has the initial user message.
	// (No tool call in history yet.)

	// Turn 2 (index 1): history includes the bash tool call and full result.
	// The bash output should still be full here (minimization hasn't happened yet).
	req2 := reqs[1]
	var bashResultFull string
	for _, msg := range req2.Messages {
		if msg.Role == "tool" && msg.ToolCallID == "call_bash_1" {
			bashResultFull = msg.Content
		}
	}
	assert.Equal(t, bashOutput, bashResultFull, "bash result should be full before minimization")

	// Turn 3 (index 2): history now includes the minimized bash result.
	req3 := reqs[2]
	var bashResultMinimized string
	var bashArgsMinimized string
	for _, msg := range req3.Messages {
		if msg.Role == "tool" && msg.ToolCallID == "call_bash_1" {
			bashResultMinimized = msg.Content
		}
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.ID == "call_bash_1" {
					bashArgsMinimized = tc.Function.Arguments
				}
			}
		}
	}
	assert.Contains(t, bashResultMinimized, minimizeSummary,
		"bash result in turn 3 should be the compact summary")
	assert.NotContains(t, bashResultMinimized, bashOutput,
		"full bash output should not appear in turn 3 history")
	assert.Equal(t, `{"_minimized":true}`, bashArgsMinimized,
		"bash args in turn 3 should be the minimized placeholder")
}

// TestAgentToolMinimization_MinimizeExcludedWhenNothingPending verifies that
// minimize_tool_result is absent from the tools list when there are no
// unminimized results.
func TestAgentToolMinimization_MinimizeExcludedWhenNothingPending(t *testing.T) {
	const minimizeArgs = `{"tool_call_id":"call_bash_1","summary":"Echo returned hi","key_findings":["Output: hi"]}`

	cs := newCaptureServer(t, []string{
		toolCallResp("call_bash_1", "bash", `{"command":"echo hi"}`),
		toolCallResp("call_min_1", "minimize_tool_result", minimizeArgs),
		chatResp("Output was: hi"),
	})

	workDir := t.TempDir()
	reg := tools.DefaultRegistry(workDir)
	store := aicli.NewToolResultStore()
	reg.Register(tools.NewMinimizeToolResult(store.Minimize))
	reg.Register(tools.NewExpandToolResult(store.Expand))

	client := aicli.NewClient(cs.srv.URL, "", "test-model")
	client.MaxRetries = 0

	agent := aicli.New(aicli.Config{
		Client:   client,
		Registry: reg,
		Store:    store,
	})

	_, err := agent.Run(context.Background(), "", "Echo hi.")
	require.NoError(t, err)

	reqs := cs.Requests()
	require.Len(t, reqs, 3)

	toolNames := func(req aicli.ChatRequest) []string {
		names := make([]string, 0, len(req.Tools))
		for _, td := range req.Tools {
			names = append(names, td.Function.Name)
		}
		return names
	}

	// Turn 1: no tool results exist yet → minimize_tool_result is excluded.
	assert.NotContains(t, toolNames(reqs[0]), "minimize_tool_result",
		"minimize_tool_result should be absent before any tool calls happen")

	// Turn 2: bash ran and its result is unminimized → minimize_tool_result is included.
	assert.Contains(t, toolNames(reqs[1]), "minimize_tool_result",
		"minimize_tool_result should be in tools list when there are unminimized results")

	// Turn 3: minimize_tool_result was called → all results minimized → excluded again.
	assert.NotContains(t, toolNames(reqs[2]), "minimize_tool_result",
		"minimize_tool_result should be excluded once all results are minimized")

	// expand_tool_result should always be present.
	assert.Contains(t, toolNames(reqs[0]), "expand_tool_result")
	assert.Contains(t, toolNames(reqs[2]), "expand_tool_result")
}

// TestAgentToolExpand_RestoresFullContent verifies the expand_tool_result flow:
//  1. bash → minimize → answer (with full content in turn 2)
//  2. model calls expand_tool_result → gets full content back immediately
//  3. next history send includes that ID at full fidelity
//  4. after that turn, collapses again
func TestAgentToolExpand_RestoresFullContent(t *testing.T) {
	const bashOutput = "hello world\n"
	const minArgs = `{"tool_call_id":"call_bash_1","summary":"Echo greeting","key_findings":["Output: hello world"]}`
	const expArgs = `{"tool_call_ids":["call_bash_1"]}`

	cs := newCaptureServer(t, []string{
		// Turn 1: bash call
		toolCallResp("call_bash_1", "bash", `{"command":"echo hello world"}`),
		// Turn 2: minimize
		toolCallResp("call_min_1", "minimize_tool_result", minArgs),
		// Turn 3: expand_tool_result
		toolCallResp("call_exp_1", "expand_tool_result", expArgs),
		// Turn 4: final answer (sees expanded history)
		chatResp("Full output confirmed: hello world"),
	})

	workDir := t.TempDir()
	reg := tools.DefaultRegistry(workDir)
	store := aicli.NewToolResultStore()
	reg.Register(tools.NewMinimizeToolResult(store.Minimize))
	reg.Register(tools.NewExpandToolResult(store.Expand))

	client := aicli.NewClient(cs.srv.URL, "", "test-model")
	client.MaxRetries = 0

	agent := aicli.New(aicli.Config{
		Client:   client,
		Registry: reg,
		Store:    store,
	})

	result, err := agent.Run(context.Background(), "", "Run echo and verify output.")
	require.NoError(t, err)
	assert.Contains(t, result, "hello world")

	reqs := cs.Requests()
	require.Len(t, reqs, 4, "expected 4 LLM turns")

	findToolContent := func(req aicli.ChatRequest, callID string) string {
		for _, msg := range req.Messages {
			if msg.Role == "tool" && msg.ToolCallID == callID {
				return msg.Content
			}
		}
		return ""
	}
	findToolArgs := func(req aicli.ChatRequest, callID string) string {
		for _, msg := range req.Messages {
			if msg.Role == "assistant" {
				for _, tc := range msg.ToolCalls {
					if tc.ID == callID {
						return tc.Function.Arguments
					}
				}
			}
		}
		return ""
	}

	// Turn 4 (index 3): expand was called in turn 3 → bash should appear at full
	// fidelity in the history sent for turn 4.
	req4 := reqs[3]
	content4 := findToolContent(req4, "call_bash_1")
	assert.Equal(t, bashOutput, content4,
		"bash output should be full in turn 4 (expanded)")
	args4 := findToolArgs(req4, "call_bash_1")
	assert.NotEqual(t, `{"_minimized":true}`, args4,
		"bash args should NOT be the minimized placeholder in turn 4")

	// Verify that expand_tool_result's own result contains full content.
	var expandResult string
	for _, msg := range reqs[3].Messages {
		if msg.Role == "tool" && msg.ToolCallID == "call_exp_1" {
			expandResult = msg.Content
		}
	}
	assert.Contains(t, expandResult, bashOutput,
		"expand_tool_result result should contain the full bash output")
}

// TestMinimizeToolResult_Execute verifies the tool wrapper in isolation.
func TestMinimizeToolResult_Execute(t *testing.T) {
	var gotID, gotSummary string
	var gotFindings []string

	tool := tools.NewMinimizeToolResult(func(id, summary string, findings []string) error {
		gotID = id
		gotSummary = summary
		gotFindings = findings
		return nil
	})

	result, err := tool.Execute(context.Background(),
		json.RawMessage(`{"tool_call_id":"call_1","summary":"foo bar","key_findings":["a","b"]}`))
	require.NoError(t, err)
	assert.Contains(t, result, "Minimized")
	assert.Equal(t, "call_1", gotID)
	assert.Equal(t, "foo bar", gotSummary)
	assert.Equal(t, []string{"a", "b"}, gotFindings)
}

// TestMinimizeToolResult_Execute_MissingID verifies that a missing tool_call_id
// returns an error.
func TestMinimizeToolResult_Execute_MissingID(t *testing.T) {
	tool := tools.NewMinimizeToolResult(func(id, summary string, findings []string) error {
		return nil
	})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"summary":"foo","key_findings":[]}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool_call_id")
}

// TestExpandToolResult_Execute verifies the tool wrapper in isolation.
func TestExpandToolResult_Execute(t *testing.T) {
	var gotIDs []string
	tool := tools.NewExpandToolResult(func(ids []string) (string, error) {
		gotIDs = ids
		return "full content", nil
	})

	result, err := tool.Execute(context.Background(),
		json.RawMessage(`{"tool_call_ids":["call_1","call_2"]}`))
	require.NoError(t, err)
	assert.Equal(t, "full content", result)
	assert.Equal(t, []string{"call_1", "call_2"}, gotIDs)
}

// TestExpandToolResult_Execute_EmptyIDs verifies that an empty ids list errors.
func TestExpandToolResult_Execute_EmptyIDs(t *testing.T) {
	tool := tools.NewExpandToolResult(func(ids []string) (string, error) {
		return "", nil
	})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"tool_call_ids":[]}`))
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "empty")
}

// TestAgentToolMinimization_NoStoreBackwardCompat verifies that an agent
// without a Store configured behaves identically to the original agent loop.
func TestAgentToolMinimization_NoStoreBackwardCompat(t *testing.T) {
	cs := newCaptureServer(t, []string{
		toolCallResp("call_bash_1", "bash", `{"command":"echo hi"}`),
		chatResp("Output: hi"),
	})

	workDir := t.TempDir()
	reg := tools.DefaultRegistry(workDir)

	client := aicli.NewClient(cs.srv.URL, "", "test-model")
	client.MaxRetries = 0

	// No Store configured — agent runs in classic mode.
	agent := aicli.New(aicli.Config{
		Client:   client,
		Registry: reg,
	})

	result, err := agent.Run(context.Background(), "", "Echo hi.")
	require.NoError(t, err)
	assert.Contains(t, result, "hi")

	reqs := cs.Requests()
	require.Len(t, reqs, 2)

	// Turn 2 should have the full bash output in history (no minimization).
	for _, msg := range reqs[1].Messages {
		if msg.Role == "tool" && msg.ToolCallID == "call_bash_1" {
			assert.Equal(t, "hi\n", msg.Content)
		}
	}
}
