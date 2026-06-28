package aicli

import (
	"fmt"
	"strings"
)

// toolResultEntry holds the original and minimized versions of a single tool call.
type toolResultEntry struct {
	toolName    string
	fullArgs    string
	fullOutput  string
	summary     string
	keyFindings []string
	minimized   bool
}

// ToolResultStore tracks tool call results and their model-provided summaries so
// the agent can collapse large payloads in subsequent history sends.
type ToolResultStore struct {
	entries  map[string]*toolResultEntry // keyed by tool_call_id
	expanded map[string]bool             // IDs to show at full fidelity on the next history send
}

// NewToolResultStore creates an empty ToolResultStore.
func NewToolResultStore() *ToolResultStore {
	return &ToolResultStore{
		entries:  make(map[string]*toolResultEntry),
		expanded: make(map[string]bool),
	}
}

// Store records the full arguments and output for a new tool call before the
// model has minimized it.
func (s *ToolResultStore) Store(toolCallID, toolName, fullArgs, fullOutput string) {
	s.entries[toolCallID] = &toolResultEntry{
		toolName:   toolName,
		fullArgs:   fullArgs,
		fullOutput: fullOutput,
	}
}

// Minimize records the model's compact summary and key findings for a tool call
// and marks it as minimized. Future history sends use the compact form.
func (s *ToolResultStore) Minimize(toolCallID, summary string, keyFindings []string) error {
	e, ok := s.entries[toolCallID]
	if !ok {
		return fmt.Errorf("tool_call_id %q not found in store", toolCallID)
	}
	e.summary = summary
	e.keyFindings = keyFindings
	e.minimized = true
	return nil
}

// HasUnminimized reports whether any stored tool call has not yet been minimized.
func (s *ToolResultStore) HasUnminimized() bool {
	for _, e := range s.entries {
		if !e.minimized {
			return true
		}
	}
	return false
}

// MarkExpanded marks the given tool_call_ids to be shown at full fidelity on
// the next history send. Called when expand_tool_result is invoked.
func (s *ToolResultStore) MarkExpanded(ids []string) {
	for _, id := range ids {
		s.expanded[id] = true
	}
}

// TakeExpanded returns the current expanded set and resets it. The caller
// applies the returned set to TransformHistory for the current turn only.
func (s *ToolResultStore) TakeExpanded() map[string]bool {
	current := s.expanded
	s.expanded = make(map[string]bool)
	return current
}

// FullContent returns the original arguments, output, and tool name for a
// tool_call_id. ok is false when the ID is unknown.
func (s *ToolResultStore) FullContent(toolCallID string) (args, output, toolName string, ok bool) {
	e, exists := s.entries[toolCallID]
	if !exists {
		return "", "", "", false
	}
	return e.fullArgs, e.fullOutput, e.toolName, true
}

// Expand marks IDs as expanded for the next history send and returns a formatted
// string with the full content for each. This is the expand_tool_result callback.
func (s *ToolResultStore) Expand(ids []string) (string, error) {
	s.MarkExpanded(ids)
	var parts []string
	for _, id := range ids {
		args, output, name, ok := s.FullContent(id)
		if !ok {
			parts = append(parts, fmt.Sprintf("ID %s: not found in store", id))
			continue
		}
		parts = append(parts, fmt.Sprintf("Tool call %s (%s)\nArguments: %s\nOutput:\n%s", id, name, args, output))
	}
	if len(parts) == 0 {
		return "No content found for the given IDs.", nil
	}
	return strings.Join(parts, "\n\n---\n\n"), nil
}

// TransformHistory returns a copy of history where minimized tool call entries
// are replaced by their compact summaries. IDs in expandedIDs are kept in full.
func (s *ToolResultStore) TransformHistory(history []Message, expandedIDs map[string]bool) []Message {
	if len(s.entries) == 0 {
		return history
	}
	result := make([]Message, len(history))
	copy(result, history)
	for i, msg := range result {
		switch msg.Role {
		case "assistant":
			if len(msg.ToolCalls) == 0 {
				continue
			}
			newCalls := make([]ToolCall, len(msg.ToolCalls))
			copy(newCalls, msg.ToolCalls)
			changed := false
			for j, tc := range newCalls {
				e, ok := s.entries[tc.ID]
				if !ok || !e.minimized || expandedIDs[tc.ID] {
					continue
				}
				newCalls[j].Function.Arguments = `{"_minimized":true}`
				changed = true
			}
			if changed {
				result[i].ToolCalls = newCalls
			}
		case "tool":
			e, ok := s.entries[msg.ToolCallID]
			if !ok || !e.minimized || expandedIDs[msg.ToolCallID] {
				continue
			}
			result[i].Content = compactContent(e)
		}
	}
	return result
}

// compactContent formats the key findings for a minimized tool result entry.
func compactContent(e *toolResultEntry) string {
	var sb strings.Builder
	sb.WriteString(e.summary)
	if len(e.keyFindings) > 0 {
		sb.WriteString("\nKey findings:")
		for _, f := range e.keyFindings {
			sb.WriteString("\n- ")
			sb.WriteString(f)
		}
	}
	return sb.String()
}
