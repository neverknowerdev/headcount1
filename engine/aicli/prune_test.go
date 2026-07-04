package aicli

import (
	"strings"
	"testing"
)

// TestPruneHistoryTruncatesStaleToolResults: bulky tool results older than
// the last two assistant turns are truncated in the request payload; recent
// ones are kept intact and the in-memory history is untouched.
func TestPruneHistoryTruncatesStaleToolResults(t *testing.T) {
	big := strings.Repeat("x", staleToolResultThreshold+1000)
	history := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "turn1"},
		{Role: "tool", Name: "codegraph_explore", Content: big},
		{Role: "assistant", Content: "turn2"},
		{Role: "tool", Name: "read", Content: big},
		{Role: "assistant", Content: "turn3"},
		{Role: "tool", Name: "grep", Content: big},
	}

	pruned, truncated := pruneHistory(history)

	if truncated != 1 {
		t.Errorf("truncated = %d, want 1", truncated)
	}
	if len(pruned[3].Content) >= len(big) {
		t.Errorf("stale tool result (before 2nd-last assistant turn) should be truncated")
	}
	if !strings.Contains(pruned[3].Content, "pruned") {
		t.Errorf("truncation marker missing")
	}
	// Results at/after the 2nd-last assistant turn stay intact.
	if pruned[5].Content != big || pruned[7].Content != big {
		t.Errorf("fresh tool results must be kept intact")
	}
	// Original history untouched.
	if history[3].Content != big {
		t.Errorf("pruneHistory must not mutate the original history")
	}
}

// TestPruneHistoryOmitsOldMCPResults keeps the existing MCP behavior.
func TestPruneHistoryOmitsOldMCPResults(t *testing.T) {
	history := []Message{
		{Role: "assistant", Content: "a1"},
		{Role: "tool", Name: "call_mcp_tool", Content: "mcp result"},
		{Role: "assistant", Content: "a2"},
		{Role: "assistant", Content: "a3"},
	}
	pruned, truncated := pruneHistory(history)
	if pruned[1].Content != "[omitted]" {
		t.Errorf("old MCP result should be omitted, got %q", pruned[1].Content)
	}
	if truncated != 1 {
		t.Errorf("truncated = %d, want 1", truncated)
	}
}

// TestFilterWildcard: prefix wildcards select tool families.
func TestFilterWildcard(t *testing.T) {
	if !nameMatchesFilter("codegraph_explore", []string{"codegraph_*"}) {
		t.Error("prefix wildcard should match")
	}
	if nameMatchesFilter("read", []string{"codegraph_*"}) {
		t.Error("non-matching name must not match")
	}
	if !nameMatchesFilter("read", []string{"read"}) {
		t.Error("exact match should match")
	}
}
