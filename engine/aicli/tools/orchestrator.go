package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// OrchestratorSession is deliberately a small wire model. The orchestrator
// receives authoritative state, not worker transcripts or filesystem access.
type OrchestratorSession struct {
	ID                int32  `json:"id"`
	Name              string `json:"name"`
	TaskID            int32  `json:"task_id"`
	Agent             string `json:"agent"`
	ParentRunID       *int32 `json:"parent_run_id,omitempty"`
	Status            string `json:"status"`
	CurrentStatus     string `json:"current_status,omitempty"`
	LastMessageTime   string `json:"last_message_time,omitempty"`
	WaitReason        string `json:"wait_reason,omitempty"`
	RecoveryAttempts  int    `json:"recovery_attempts"`
	StopCause         string `json:"stop_cause,omitempty"`
	ResultDescription string `json:"result_description,omitempty"`
	Error             string `json:"error,omitempty"`
}

type OrchestratorCallbacks struct {
	GetSessions      func(context.Context) ([]OrchestratorSession, error)
	GetSessionStatus func(context.Context, int32) (OrchestratorSession, error)
	AskTaskOwner     func(context.Context, int32, string) (string, error)
	RunNewSession    func(context.Context, *int32, string) (string, error)
	StopSession      func(context.Context, int32, string) (string, error)
	ForkSession      func(context.Context, int32, int64) (string, error)
}

type orchestratorTool struct {
	name aicli.ToolName
	def  aicli.ToolDef
	fn   func(context.Context, json.RawMessage) (string, error)
}

func (t *orchestratorTool) Def() aicli.ToolDef { return t.def }
func (t *orchestratorTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.fn(ctx, args)
}

func orchestratorDef(name aicli.ToolName, description, schema string) aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
		Name: string(name), Description: description, Parameters: json.RawMessage(schema),
	}}
}

// NewOrchestratorRegistry creates exactly the management surface permitted to
// a sidecar. It intentionally does not call DefaultRegistry.
func NewOrchestratorRegistry(cb OrchestratorCallbacks) *aicli.Registry {
	r := aicli.NewRegistry()
	r.Register(&orchestratorTool{
		name: aicli.ToolGetSessions,
		def:  orchestratorDef(aicli.ToolGetSessions, "List every worker session in this task execution, including nested sessions.", `{"type":"object","properties":{}}`),
		fn: func(ctx context.Context, _ json.RawMessage) (string, error) {
			v, err := cb.GetSessions(ctx)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(v)
			return string(b), err
		},
	})
	r.Register(&orchestratorTool{
		name: aicli.ToolGetSessionStatus,
		def:  orchestratorDef(aicli.ToolGetSessionStatus, "Inspect one worker session's authoritative status, heartbeat, wait reason, error, and recovery state.", `{"type":"object","properties":{"session_id":{"type":"integer"}},"required":["session_id"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SessionID int32 `json:"session_id"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("get_session_status: %w", err)
			}
			if p.SessionID <= 0 {
				return "", fmt.Errorf("session_id must be positive")
			}
			v, err := cb.GetSessionStatus(ctx, p.SessionID)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(v)
			return string(b), err
		},
	})
	r.Register(&orchestratorTool{
		name: aicli.ToolAskTaskOwner,
		def:  orchestratorDef(aicli.ToolAskTaskOwner, "Ask the owning worker agent for a status, blocker, or decision. The answer arrives asynchronously.", `{"type":"object","properties":{"session_id":{"type":"integer"},"question":{"type":"string"}},"required":["session_id","question"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SessionID int32  `json:"session_id"`
				Question  string `json:"question"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("ask_task_owner: %w", err)
			}
			if p.SessionID <= 0 || p.Question == "" {
				return "", fmt.Errorf("session_id and question are required")
			}
			return cb.AskTaskOwner(ctx, p.SessionID, p.Question)
		},
	})
	r.Register(&orchestratorTool{
		name: aicli.ToolRunNewSession,
		def:  orchestratorDef(aicli.ToolRunNewSession, "Start a bounded replacement for a failed worker session, preserving replacement lineage.", `{"type":"object","properties":{"source_session_id":{"type":"integer"},"reason":{"type":"string"}},"required":["reason"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SourceSessionID *int32 `json:"source_session_id"`
				Reason          string `json:"reason"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("run_new_session: %w", err)
			}
			if p.Reason == "" {
				return "", fmt.Errorf("reason is required")
			}
			return cb.RunNewSession(ctx, p.SourceSessionID, p.Reason)
		},
	})
	r.Register(&orchestratorTool{
		name: aicli.ToolStopSession,
		def:  orchestratorDef(aicli.ToolStopSession, "Stop one unhealthy worker session. Never use this for intentional human or owner waits.", `{"type":"object","properties":{"session_id":{"type":"integer"},"reason":{"type":"string"}},"required":["session_id","reason"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SessionID int32  `json:"session_id"`
				Reason    string `json:"reason"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("stop_session: %w", err)
			}
			if p.SessionID <= 0 || p.Reason == "" {
				return "", fmt.Errorf("session_id and reason are required")
			}
			return cb.StopSession(ctx, p.SessionID, p.Reason)
		},
	})
	r.Register(&orchestratorTool{
		name: aicli.ToolForkSession,
		def:  orchestratorDef(aicli.ToolForkSession, "Fork a worker from a persisted safe message boundary. This does not roll back side effects.", `{"type":"object","properties":{"session_id":{"type":"integer"},"fork_message_id":{"type":"integer"}},"required":["session_id","fork_message_id"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SessionID     int32 `json:"session_id"`
				ForkMessageID int64 `json:"fork_message_id"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("fork_session: %w", err)
			}
			if p.SessionID <= 0 || p.ForkMessageID <= 0 {
				return "", fmt.Errorf("session_id and fork_message_id must be positive")
			}
			return cb.ForkSession(ctx, p.SessionID, p.ForkMessageID)
		},
	})
	return r
}
