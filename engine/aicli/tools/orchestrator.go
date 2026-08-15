package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// ManagedSessionSummary is the lifecycle snapshot returned by get_sessions.
// Worker progress reports are returned by get_session_last_run_status instead.
type ManagedSessionSummary struct {
	ID                int32  `json:"id"`
	Name              string `json:"name"`
	TaskID            int32  `json:"task_id"`
	AgentID           int32  `json:"agent_id"`
	AgentName         string `json:"agent_name"`
	LifecycleStatus   string `json:"status"`
	LastMessageTime   string `json:"last_message_time,omitempty"`
	WaitReason        string `json:"wait_reason,omitempty"`
	RecoveryAttempts  int    `json:"recovery_attempts"`
	StopCause         string `json:"stop_cause,omitempty"`
	ResultDescription string `json:"result_description,omitempty"`
	Error             string `json:"error,omitempty"`
}

// ManagedSessionStatusReport is the latest progress line emitted by a worker's
// report_status tool. It deliberately does not expose the lifecycle status.
type ManagedSessionStatusReport struct {
	ID                     int32  `json:"id"`
	Name                   string `json:"name"`
	TaskID                 int32  `json:"task_id"`
	AgentID                int32  `json:"agent_id"`
	AgentName              string `json:"agent_name"`
	LastReportedStatus     string `json:"last_reported_status,omitempty"`
	LastReportedAt         string `json:"last_reported_at,omitempty"`
	LastReportedMessageID  int64  `json:"last_reported_message_id,omitempty"`
	StatusReportStale      bool   `json:"status_report_stale"`
	StatusRefreshRequested bool   `json:"status_refresh_requested"`
}

// OrchestratorToolName is intentionally private to this registry. These
// controls are never part of a worker's configurable tool surface.
type OrchestratorToolName string

const (
	OrchestratorToolGetSessions             OrchestratorToolName = "get_sessions"
	OrchestratorToolGetSessionLastRunStatus OrchestratorToolName = "get_session_last_run_status"
	OrchestratorToolAskSessionAgent         OrchestratorToolName = "ask_session_agent"
	OrchestratorToolRunNewSession           OrchestratorToolName = "run_new_session"
	OrchestratorToolStopSession             OrchestratorToolName = "stop_session"
	OrchestratorToolForkSession             OrchestratorToolName = "fork_session"
)

type OrchestratorCallbacks struct {
	GetSessions             func(context.Context) ([]ManagedSessionSummary, error)
	GetSessionLastRunStatus func(context.Context, int32) (ManagedSessionStatusReport, error)
	AskSessionAgent         func(context.Context, int32, string) (string, error)
	RunNewSession           func(context.Context, *int32, *int32, string, bool) (string, error)
	StopSession             func(context.Context, int32, string) (string, error)
	ForkSession             func(context.Context, int32, int64) (string, error)
}

type orchestratorManagementTool struct {
	name OrchestratorToolName
	def  aicli.ToolDef
	fn   func(context.Context, json.RawMessage) (string, error)
}

func (t *orchestratorManagementTool) Def() aicli.ToolDef { return t.def }
func (t *orchestratorManagementTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.fn(ctx, args)
}

func orchestratorDef(name OrchestratorToolName, description, schema string) aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
		Name: string(name), Description: description, Parameters: json.RawMessage(schema),
	}}
}

// NewOrchestratorRegistry creates exactly the management surface permitted to
// a sidecar. It intentionally does not call DefaultRegistry.
func NewOrchestratorRegistry(cb OrchestratorCallbacks) *aicli.Registry {
	r := aicli.NewRegistry()
	r.Register(&orchestratorManagementTool{
		name: OrchestratorToolGetSessions,
		def:  orchestratorDef(OrchestratorToolGetSessions, "List every worker session in this task execution, including nested sessions.", `{"type":"object","properties":{}}`),
		fn: func(ctx context.Context, _ json.RawMessage) (string, error) {
			v, err := cb.GetSessions(ctx)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(v)
			return string(b), err
		},
	})
	r.Register(&orchestratorManagementTool{
		name: OrchestratorToolGetSessionLastRunStatus,
		def:  orchestratorDef(OrchestratorToolGetSessionLastRunStatus, "Return the latest status line reported by a worker through report_status and when it was reported. If it is stale, request a fresh report.", `{"type":"object","properties":{"session_id":{"type":"integer"}},"required":["session_id"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SessionID int32 `json:"session_id"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("get_session_last_run_status: %w", err)
			}
			if p.SessionID <= 0 {
				return "", fmt.Errorf("session_id must be positive")
			}
			v, err := cb.GetSessionLastRunStatus(ctx, p.SessionID)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(v)
			return string(b), err
		},
	})
	r.Register(&orchestratorManagementTool{
		name: OrchestratorToolAskSessionAgent,
		def:  orchestratorDef(OrchestratorToolAskSessionAgent, "Send a question to the agent running a managed session. Delivery is asynchronous and will be added before that session's next provider request.", `{"type":"object","properties":{"session_id":{"type":"integer"},"question":{"type":"string"}},"required":["session_id","question"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SessionID int32  `json:"session_id"`
				Question  string `json:"question"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("ask_session_agent: %w", err)
			}
			if p.SessionID <= 0 || p.Question == "" {
				return "", fmt.Errorf("session_id and question are required")
			}
			return cb.AskSessionAgent(ctx, p.SessionID, p.Question)
		},
	})
	r.Register(&orchestratorManagementTool{
		name: OrchestratorToolRunNewSession,
		def:  orchestratorDef(OrchestratorToolRunNewSession, "Start a new session for an agent related to this task. Optionally replace a source session; include_task_context controls whether the task prompt is included. The reason is also the session instruction.", `{"type":"object","properties":{"source_session_id":{"type":"integer"},"agent_id":{"type":"integer"},"reason":{"type":"string"},"include_task_context":{"type":"boolean","default":true}},"required":["reason"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SourceSessionID    *int32 `json:"source_session_id"`
				AgentID            *int32 `json:"agent_id"`
				Reason             string `json:"reason"`
				IncludeTaskContext *bool  `json:"include_task_context"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("run_new_session: %w", err)
			}
			if p.Reason == "" {
				return "", fmt.Errorf("reason is required")
			}
			if p.SourceSessionID != nil && *p.SourceSessionID <= 0 {
				return "", fmt.Errorf("source_session_id must be positive")
			}
			if p.AgentID != nil && *p.AgentID <= 0 {
				return "", fmt.Errorf("agent_id must be positive")
			}
			includeTaskContext := true
			if p.IncludeTaskContext != nil {
				includeTaskContext = *p.IncludeTaskContext
			}
			return cb.RunNewSession(ctx, p.SourceSessionID, p.AgentID, p.Reason, includeTaskContext)
		},
	})
	r.Register(&orchestratorManagementTool{
		name: OrchestratorToolStopSession,
		def:  orchestratorDef(OrchestratorToolStopSession, "Stop one unhealthy worker session. Never use this for intentional human or owner waits.", `{"type":"object","properties":{"session_id":{"type":"integer"},"reason":{"type":"string"}},"required":["session_id","reason"]}`),
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
	r.Register(&orchestratorManagementTool{
		name: OrchestratorToolForkSession,
		def:  orchestratorDef(OrchestratorToolForkSession, "Fork a worker from a persisted safe message boundary. This does not roll back side effects.", `{"type":"object","properties":{"session_id":{"type":"integer"},"fork_message_id":{"type":"integer"}},"required":["session_id","fork_message_id"]}`),
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
