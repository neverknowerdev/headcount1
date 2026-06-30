package aicli_test

import (
	"testing"

	"agent-orchestrator/engine/aicli"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToolResultStore_StoreAndHasUnminimized verifies that Store adds an entry
// and HasUnminimized returns true until the entry is minimized.
func TestToolResultStore_StoreAndHasUnminimized(t *testing.T) {
	s := aicli.NewToolResultStore()
	assert.False(t, s.HasUnminimized(), "empty store should have no unminimized entries")

	s.Store("tc_1", "bash", `{"command":"echo hi"}`, "hi\n")
	assert.True(t, s.HasUnminimized())

	require.NoError(t, s.Minimize("tc_1", "Echo returned hi"))
	assert.False(t, s.HasUnminimized())
}

// TestToolResultStore_UnminimizedIDs verifies that UnminimizedIDs returns only
// entries not yet minimized, sorted stably.
func TestToolResultStore_UnminimizedIDs(t *testing.T) {
	s := aicli.NewToolResultStore()
	assert.Empty(t, s.UnminimizedIDs())

	s.Store("tc_2", "bash", `{}`, "b")
	s.Store("tc_1", "bash", `{}`, "a")
	ids := s.UnminimizedIDs()
	assert.Equal(t, []string{"tc_1", "tc_2"}, ids, "IDs should be sorted")

	require.NoError(t, s.Minimize("tc_1", "summary a"))
	ids = s.UnminimizedIDs()
	assert.Equal(t, []string{"tc_2"}, ids)
}

// TestToolResultStore_MinimizeUnknownID verifies an error is returned for an
// unknown tool_call_id.
func TestToolResultStore_MinimizeUnknownID(t *testing.T) {
	s := aicli.NewToolResultStore()
	err := s.Minimize("no_such_id", "summary")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no_such_id")
}

// TestToolResultStore_FullContent returns the original args and output.
func TestToolResultStore_FullContent(t *testing.T) {
	s := aicli.NewToolResultStore()
	s.Store("tc_1", "bash", `{"command":"echo hi"}`, "hi\n")

	args, output, name, ok := s.FullContent("tc_1")
	require.True(t, ok)
	assert.Equal(t, `{"command":"echo hi"}`, args)
	assert.Equal(t, "hi\n", output)
	assert.Equal(t, "bash", name)

	_, _, _, ok = s.FullContent("unknown")
	assert.False(t, ok)
}

// TestToolResultStore_TransformHistory_CollapseMinimized verifies that a
// minimized entry's tool result content is replaced by the compact summary.
func TestToolResultStore_TransformHistory_CollapseMinimized(t *testing.T) {
	// Use a distinctive output string that won't appear in the compact summary.
	const rawOutput = "UNIQUE_RAW_OUTPUT_12345\n"
	s := aicli.NewToolResultStore()
	s.Store("tc_1", "bash", `{"command":"echo hello"}`, rawOutput)
	require.NoError(t, s.Minimize("tc_1", "Echo returned greeting"))

	history := []aicli.Message{
		{
			Role: "assistant",
			ToolCalls: []aicli.ToolCall{
				{ID: "tc_1", Type: "function", Function: aicli.FuncCall{Name: "bash", Arguments: `{"command":"echo hello"}`}},
			},
		},
		{Role: "tool", ToolCallID: "tc_1", Name: "bash", Content: rawOutput},
	}

	transformed := s.TransformHistory(history, nil)

	// Tool call args should be replaced with the minimized placeholder.
	assert.Equal(t, `{"_minimized":true}`, transformed[0].ToolCalls[0].Function.Arguments)
	// Tool result content should be the compact summary.
	assert.Equal(t, "Echo returned greeting", transformed[1].Content)
	// The raw output must not appear in the compact form.
	assert.NotContains(t, transformed[1].Content, rawOutput)
}

// TestToolResultStore_TransformHistory_ExpandedNotCollapsed verifies that an
// ID in the expandedIDs set is kept at full fidelity.
func TestToolResultStore_TransformHistory_ExpandedNotCollapsed(t *testing.T) {
	s := aicli.NewToolResultStore()
	s.Store("tc_1", "bash", `{"command":"echo hi"}`, "hi\n")
	require.NoError(t, s.Minimize("tc_1", "Echo returned hi"))

	expanded := map[string]bool{"tc_1": true}

	history := []aicli.Message{
		{
			Role: "assistant",
			ToolCalls: []aicli.ToolCall{
				{ID: "tc_1", Type: "function", Function: aicli.FuncCall{Name: "bash", Arguments: `{"command":"echo hi"}`}},
			},
		},
		{Role: "tool", ToolCallID: "tc_1", Name: "bash", Content: "hi\n"},
	}

	transformed := s.TransformHistory(history, expanded)

	// Args and content should be unchanged.
	assert.Equal(t, `{"command":"echo hi"}`, transformed[0].ToolCalls[0].Function.Arguments)
	assert.Equal(t, "hi\n", transformed[1].Content)
}

// TestToolResultStore_TransformHistory_NotMinimizedKeptFull verifies that an
// entry stored but not yet minimized is kept at full fidelity.
func TestToolResultStore_TransformHistory_NotMinimizedKeptFull(t *testing.T) {
	s := aicli.NewToolResultStore()
	s.Store("tc_1", "bash", `{"command":"echo hi"}`, "hi\n")
	// not minimized

	history := []aicli.Message{
		{
			Role: "assistant",
			ToolCalls: []aicli.ToolCall{
				{ID: "tc_1", Type: "function", Function: aicli.FuncCall{Name: "bash", Arguments: `{"command":"echo hi"}`}},
			},
		},
		{Role: "tool", ToolCallID: "tc_1", Name: "bash", Content: "hi\n"},
	}

	transformed := s.TransformHistory(history, nil)
	assert.Equal(t, `{"command":"echo hi"}`, transformed[0].ToolCalls[0].Function.Arguments)
	assert.Equal(t, "hi\n", transformed[1].Content)
}

// TestToolResultStore_TakeExpanded clears the expanded set after taking it.
func TestToolResultStore_TakeExpanded(t *testing.T) {
	s := aicli.NewToolResultStore()
	s.MarkExpanded([]string{"tc_1", "tc_2"})

	first := s.TakeExpanded()
	assert.True(t, first["tc_1"])
	assert.True(t, first["tc_2"])

	second := s.TakeExpanded()
	assert.Empty(t, second, "expanded set should be cleared after TakeExpanded")
}

// TestToolResultStore_Expand_MarksExpandedAndReturnsContent verifies that
// Expand marks IDs for next-turn expansion and returns full content.
func TestToolResultStore_Expand_MarksExpandedAndReturnsContent(t *testing.T) {
	s := aicli.NewToolResultStore()
	s.Store("tc_1", "bash", `{"command":"echo hi"}`, "hi\n")
	require.NoError(t, s.Minimize("tc_1", "summary"))

	result, err := s.Expand([]string{"tc_1"})
	require.NoError(t, err)
	assert.Contains(t, result, `{"command":"echo hi"}`)
	assert.Contains(t, result, "hi\n")
	assert.Contains(t, result, "bash")

	// ID should now be in the expanded set.
	expanded := s.TakeExpanded()
	assert.True(t, expanded["tc_1"])
}

// TestToolResultStore_Expand_UnknownID returns a not-found message but no error.
func TestToolResultStore_Expand_UnknownID(t *testing.T) {
	s := aicli.NewToolResultStore()
	result, err := s.Expand([]string{"no_such_id"})
	require.NoError(t, err)
	assert.Contains(t, result, "not found")
}

// TestToolResultStore_MultipleEntries verifies that the store handles multiple
// simultaneous entries independently.
func TestToolResultStore_MultipleEntries(t *testing.T) {
	s := aicli.NewToolResultStore()
	s.Store("tc_1", "bash", `{"command":"echo a"}`, "a\n")
	s.Store("tc_2", "bash", `{"command":"echo b"}`, "b\n")

	// Minimize only tc_1.
	require.NoError(t, s.Minimize("tc_1", "Echo returned a"))
	assert.True(t, s.HasUnminimized(), "tc_2 is still unminimized")

	require.NoError(t, s.Minimize("tc_2", "Echo returned b"))
	assert.False(t, s.HasUnminimized())

	history := []aicli.Message{
		{
			Role: "assistant",
			ToolCalls: []aicli.ToolCall{
				{ID: "tc_1", Function: aicli.FuncCall{Name: "bash", Arguments: `{"command":"echo a"}`}},
				{ID: "tc_2", Function: aicli.FuncCall{Name: "bash", Arguments: `{"command":"echo b"}`}},
			},
		},
		{Role: "tool", ToolCallID: "tc_1", Content: "a\n"},
		{Role: "tool", ToolCallID: "tc_2", Content: "b\n"},
	}

	transformed := s.TransformHistory(history, nil)
	assert.Equal(t, `{"_minimized":true}`, transformed[0].ToolCalls[0].Function.Arguments)
	assert.Equal(t, `{"_minimized":true}`, transformed[0].ToolCalls[1].Function.Arguments)
	assert.Equal(t, "Echo returned a", transformed[1].Content)
	assert.Equal(t, "Echo returned b", transformed[2].Content)
}
