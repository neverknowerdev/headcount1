package aicli

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolReplay is one completed assistant tool call from a persisted message
// history. RecordedResult is supplied so callers can safely reuse durable
// control-plane results while replaying state-changing tools.
type ToolReplay struct {
	Call           ToolCall
	RecordedResult string
}

// ToolReplayFunc applies one completed tool call to the forked environment.
type ToolReplayFunc func(context.Context, ToolReplay) error

// ToolReplayError identifies the exact call that failed while reconstructing a
// fork. The caller can return this error to the orchestrator as a normal tool
// failure, leaving the orchestrator responsible for recovery decisions.
type ToolReplayError struct {
	Call ToolCall
	Err  error
}

func (e *ToolReplayError) Error() string {
	return fmt.Sprintf("replay tool %s (%s) failed: %v", e.Call.Function.Name, e.Call.ID, e.Err)
}

func (e *ToolReplayError) Unwrap() error { return e.Err }

// ReplayCompletedToolCalls re-applies completed tool calls in their original
// order. It deliberately requires every assistant tool call to have a
// matching persisted result, so a fork can never execute an unsafe partial
// turn. Conversation messages themselves remain unchanged; the fork seeds its
// model history with the persisted messages after this side-effect replay.
func ReplayCompletedToolCalls(ctx context.Context, history []Message, replay ToolReplayFunc) error {
	if replay == nil {
		return fmt.Errorf("tool replay callback is required")
	}
	pending := make(map[string]ToolReplay)
	order := make([]string, 0)
	for index, message := range history {
		switch message.Role {
		case "assistant":
			for _, call := range message.ToolCalls {
				if call.ID == "" {
					return fmt.Errorf("message %d has a tool call without an ID", index)
				}
				if _, exists := pending[call.ID]; exists {
					return fmt.Errorf("message %d repeats pending tool call %q", index, call.ID)
				}
				pending[call.ID] = ToolReplay{Call: call}
				order = append(order, call.ID)
			}
		case "tool":
			if message.ToolCallID == "" {
				return fmt.Errorf("message %d has a tool result without a tool_call_id", index)
			}
			replayCall, exists := pending[message.ToolCallID]
			if !exists {
				return fmt.Errorf("message %d has an unmatched tool result %q", index, message.ToolCallID)
			}
			replayCall.RecordedResult = message.Content
			if err := replay(ctx, replayCall); err != nil {
				return &ToolReplayError{Call: replayCall.Call, Err: err}
			}
			delete(pending, message.ToolCallID)
		}
	}
	if len(pending) > 0 {
		for _, callID := range order {
			if replayCall, exists := pending[callID]; exists {
				return fmt.Errorf("tool call %s (%s) has no persisted result", replayCall.Call.Function.Name, replayCall.Call.ID)
			}
		}
	}
	return nil
}

// ReplayRegistryToolCall is the standard replay adapter for a runtime
// registry. It preserves the original JSON arguments and intentionally
// discards the new output; the persisted tool result remains authoritative for
// the forked conversation.
func ReplayRegistryToolCall(ctx context.Context, replay ToolReplay, registry *Registry) error {
	if registry == nil {
		return fmt.Errorf("tool registry is required")
	}
	_, err := registry.Execute(ctx, replay.Call.Function.Name, json.RawMessage(replay.Call.Function.Arguments))
	return err
}
