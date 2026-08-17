package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/engine/aicli"
)

// ManagedSessionSummary is the compact lifecycle snapshot returned by
// get_session_list.
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

// ManagedSessionChildStatus is a recursively aggregated progress snapshot for
// a session spawned by the selected session. Status is the child's own
// report, followed by the same representation for its descendants. The
// orchestrator can therefore see why a parent is waiting without flattening
// the session tree into unrelated list entries.
type ManagedSessionChildStatus struct {
	ID                    int32                       `json:"id"`
	Name                  string                      `json:"name"`
	AgentName             string                      `json:"agent_name"`
	OwnReportedStatus     string                      `json:"own_reported_status,omitempty"`
	Status                string                      `json:"status,omitempty"`
	LastReportedAt        string                      `json:"last_reported_at,omitempty"`
	LastReportedMessageID int64                       `json:"last_reported_message_id,omitempty"`
	StatusReportStale     bool                        `json:"status_report_stale"`
	ChildStatuses         []ManagedSessionChildStatus `json:"child_statuses,omitempty"`
	NestedStatusTruncated bool                        `json:"nested_status_truncated,omitempty"`
}

// ManagedSessionStatusReport is the latest progress line emitted by a worker's
// report_status tool. It deliberately does not expose the lifecycle status.
// LastReportedStatus is the selected session's own report with child status
// lines appended. OwnReportedStatus preserves the unmodified report for
// callers that need to distinguish the parent's progress from its children.
type ManagedSessionStatusReport struct {
	ID                     int32                       `json:"id"`
	Name                   string                      `json:"name"`
	TaskID                 int32                       `json:"task_id"`
	AgentID                int32                       `json:"agent_id"`
	AgentName              string                      `json:"agent_name"`
	LastReportedStatus     string                      `json:"last_reported_status,omitempty"`
	OwnReportedStatus      string                      `json:"own_reported_status,omitempty"`
	LastReportedAt         string                      `json:"last_reported_at,omitempty"`
	LastReportedMessageID  int64                       `json:"last_reported_message_id,omitempty"`
	StatusReportStale      bool                        `json:"status_report_stale"`
	StatusRefreshRequested bool                        `json:"status_refresh_requested"`
	ChildStatuses          []ManagedSessionChildStatus `json:"child_statuses,omitempty"`
	NestedStatusTruncated  bool                        `json:"nested_status_truncated,omitempty"`
}

// ManagedSessionRunStatus is one append-only report_status entry. The
// orchestrator receives the full ordered history through get_session.
type ManagedSessionRunStatus struct {
	Status     string `json:"status"`
	ReportedAt string `json:"reported_at"`
	MessageID  int64  `json:"message_id"`
}

// ManagedSessionDetails combines lifecycle information with the worker's
// progress-report history. LastRunStatus is nil when report_status has never
// been called for the selected session and it has no nested sessions.
type ManagedSessionDetails struct {
	ManagedSessionSummary
	LastRunStatus    *ManagedSessionStatusReport `json:"last_run_status"`
	RunStatusHistory []ManagedSessionRunStatus   `json:"run_status_history"`
}

// AvailableAgent is an agent the task orchestrator can select for a new
// worker session. Names are the stable input accepted by run_new_session;
// descriptions help the orchestrator choose without guessing from IDs.
type AvailableAgent struct {
	Name        string `json:"name"`
	RoleKey     string `json:"role_key,omitempty"`
	Description string `json:"description,omitempty"`
}

// OrchestratorToolName is intentionally private to this registry. These
// controls are never part of a worker's configurable tool surface.
type OrchestratorToolName string

const (
	OrchestratorToolGetSessionList OrchestratorToolName = "get_session_list"
	OrchestratorToolGetSession     OrchestratorToolName = "get_session"
	OrchestratorToolSendMessage    OrchestratorToolName = "send_message_to_session"
	OrchestratorToolRunNewSession  OrchestratorToolName = "run_new_session"
	OrchestratorToolStopSession    OrchestratorToolName = "stop_session"
	OrchestratorToolForkSession    OrchestratorToolName = "fork_session"
	OrchestratorToolAskCEO         OrchestratorToolName = "ask_ceo"
	// OrchestratorToolAskAgent is retained as a source-compatibility alias for
	// callers compiled against the pre-cutover Go API. It never registers the
	// legacy model-facing name.
	OrchestratorToolAskAgent OrchestratorToolName = OrchestratorToolSendMessage
)

type OrchestratorCallbacks struct {
	GetSessionList func(context.Context) ([]ManagedSessionSummary, error)
	GetSession     func(context.Context, int32) (ManagedSessionDetails, error)
	AskAgent       func(context.Context, int32, string) (string, error)
	RunNewSession  func(context.Context, *int32, string, string) (string, error)
	StopSession    func(context.Context, int32, string) (string, error)
	ForkSession    func(context.Context, int32, int64) (string, error)
	AnswerMessage  func(context.Context, int64, string) (string, error)
	AskCEO         func(context.Context, int32, string) (string, error)
}

type orchestratorManagementTool struct {
	name OrchestratorToolName
	def  aicli.ToolDef
	fn   func(context.Context, json.RawMessage) (string, error)
}

func RegisterOrchestratorAnswerMessage(r *aicli.Registry, fn func(context.Context, int64, string) (string, error)) {
	if fn != nil {
		r.Register(NewAnswerMessage(fn))
	}
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
		name: OrchestratorToolGetSessionList,
		def:  orchestratorDef(OrchestratorToolGetSessionList, "List every worker session in this task execution, including nested sessions. Use get_session for detailed lifecycle and status-report history.", `{"type":"object","properties":{}}`),
		fn: func(ctx context.Context, _ json.RawMessage) (string, error) {
			v, err := cb.GetSessionList(ctx)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(v)
			return string(b), err
		},
	})
	r.Register(&orchestratorManagementTool{
		name: OrchestratorToolGetSession,
		def:  orchestratorDef(OrchestratorToolGetSession, "Return one worker session's lifecycle information, the latest report_status result, recursively aggregated status for nested child sessions (up to five levels), and the complete chronological history for the selected session. If the selected session's latest report is stale, request a fresh report.", `{"type":"object","properties":{"session_id":{"type":"integer"}},"required":["session_id"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SessionID int32 `json:"session_id"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("get_session: %w", err)
			}
			if p.SessionID <= 0 {
				return "", fmt.Errorf("session_id must be positive")
			}
			v, err := cb.GetSession(ctx, p.SessionID)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(v)
			return string(b), err
		},
	})
	r.Register(&orchestratorManagementTool{
		name: OrchestratorToolSendMessage,
		def:  orchestratorDef(OrchestratorToolSendMessage, "Send a durable message to the agent running a managed session and wait for its correlated answer.", `{"type":"object","properties":{"session_id":{"type":"integer"},"message":{"type":"string"}},"required":["session_id","message"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SessionID      int32  `json:"session_id"`
				Question       string `json:"message"`
				LegacyQuestion string `json:"question"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("send_message_to_session: %w", err)
			}
			if p.Question == "" {
				p.Question = p.LegacyQuestion
			}
			if p.SessionID <= 0 || p.Question == "" {
				return "", fmt.Errorf("session_id and question are required")
			}
			return cb.AskAgent(ctx, p.SessionID, p.Question)
		},
	})
	r.Register(&orchestratorManagementTool{
		name: OrchestratorToolRunNewSession,
		def:  orchestratorDef(OrchestratorToolRunNewSession, "Start a child worker session for the selected agent. The worker receives the complete task context plus your prompt. Optionally replace a source session when recovering from a failed or unsafe execution.", `{"type":"object","properties":{"source_session_id":{"type":"integer","description":"Existing managed session to replace; omit for a new worker."},"agent_name":{"type":"string","description":"Name or role key from the available-agents list."},"prompt":{"type":"string","description":"Detailed implementation instruction, constraints, and expected handoff for the worker."}},"required":["agent_name","prompt"]}`),
		fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				SourceSessionID *int32 `json:"source_session_id"`
				AgentName       string `json:"agent_name"`
				Prompt          string `json:"prompt"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("run_new_session: %w", err)
			}
			if strings.TrimSpace(p.AgentName) == "" || strings.TrimSpace(p.Prompt) == "" {
				return "", fmt.Errorf("agent_name and prompt are required")
			}
			if p.SourceSessionID != nil && *p.SourceSessionID <= 0 {
				return "", fmt.Errorf("source_session_id must be positive")
			}
			return cb.RunNewSession(ctx, p.SourceSessionID, p.AgentName, p.Prompt)
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
	r.Register(NewAskCEO(cb.AskCEO))
	return r
}
