package tools

import (
	"context"
	"encoding/json"
	"testing"

	"agent-orchestrator/engine/aicli"
	"github.com/stretchr/testify/require"
)

func TestNewOrchestratorRegistryOnlyExposesManagementTools(t *testing.T) {
	r := NewOrchestratorRegistry(OrchestratorCallbacks{
		GetSessionList: func(context.Context) ([]ManagedSessionSummary, error) { return nil, nil },
		GetSession: func(context.Context, int32) (ManagedSessionDetails, error) {
			return ManagedSessionDetails{}, nil
		},
		AskAgent:      func(context.Context, int32, string) (string, error) { return "", nil },
		RunNewSession: func(context.Context, *int32, string, string) (string, error) { return "", nil },
		StopSession:   func(context.Context, int32, string) (string, error) { return "", nil },
		ForkSession:   func(context.Context, int32, int64) (string, error) { return "", nil },
	})
	require.Equal(t, []string{"ask_agent", "fork_session", "get_session", "get_session_list", "run_new_session", "stop_session"}, r.Names())
	require.Error(t, func() error { _, err := r.Execute(context.Background(), string(aicli.ToolWrite), nil); return err }())
}

func TestOrchestratorToolValidation(t *testing.T) {
	called := false
	r := NewOrchestratorRegistry(OrchestratorCallbacks{
		GetSessionList: func(context.Context) ([]ManagedSessionSummary, error) { return nil, nil },
		GetSession: func(context.Context, int32) (ManagedSessionDetails, error) {
			return ManagedSessionDetails{}, nil
		},
		AskAgent:      func(context.Context, int32, string) (string, error) { called = true; return "ok", nil },
		RunNewSession: func(context.Context, *int32, string, string) (string, error) { return "", nil },
		StopSession:   func(context.Context, int32, string) (string, error) { return "", nil },
		ForkSession:   func(context.Context, int32, int64) (string, error) { return "", nil },
	})
	_, err := r.Execute(context.Background(), string(OrchestratorToolAskAgent), json.RawMessage(`{"session_id":0,"question":"x"}`))
	require.Error(t, err)
	require.False(t, called)
	_, err = r.Execute(context.Background(), string(OrchestratorToolAskAgent), json.RawMessage(`{"session_id":3,"question":"status?"}`))
	require.NoError(t, err)
	require.True(t, called)
}

func TestSessionInspectionToolsReturnListAndHistory(t *testing.T) {
	gotID := int32(0)
	r := NewOrchestratorRegistry(OrchestratorCallbacks{
		GetSessionList: func(context.Context) ([]ManagedSessionSummary, error) {
			return []ManagedSessionSummary{{ID: 4, Name: "worker-4", LifecycleStatus: "running"}}, nil
		},
		GetSession: func(_ context.Context, id int32) (ManagedSessionDetails, error) {
			gotID = id
			return ManagedSessionDetails{
				ManagedSessionSummary: ManagedSessionSummary{ID: id, Name: "worker-4", LifecycleStatus: "running"},
				LastRunStatus: &ManagedSessionStatusReport{
					ID: id, OwnReportedStatus: "waiting for Coder", LastReportedStatus: "waiting for Coder. Coder status: implementing",
					LastReportedMessageID: 22,
					ChildStatuses:         []ManagedSessionChildStatus{{ID: 5, AgentName: "Coder", Status: "implementing", LastReportedMessageID: 33}},
				},
				RunStatusHistory: []ManagedSessionRunStatus{{Status: "planning", MessageID: 11}, {Status: "implementing", MessageID: 22}},
			}, nil
		},
	})
	list, err := r.Execute(context.Background(), string(OrchestratorToolGetSessionList), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Contains(t, list, `"worker-4"`)
	detail, err := r.Execute(context.Background(), string(OrchestratorToolGetSession), json.RawMessage(`{"session_id":4}`))
	require.NoError(t, err)
	require.Equal(t, int32(4), gotID)
	require.Contains(t, detail, `"last_run_status"`)
	require.Contains(t, detail, `"run_status_history"`)
	require.Contains(t, detail, `"message_id":22`)
	require.Contains(t, detail, `"child_statuses"`)
	require.Contains(t, detail, `Coder status: implementing`)
}

func TestRunNewSessionPassesPromptAndAgentName(t *testing.T) {
	var gotSource *int32
	var gotAgent, gotPrompt string
	r := NewOrchestratorRegistry(OrchestratorCallbacks{
		GetSessionList: func(context.Context) ([]ManagedSessionSummary, error) { return nil, nil },
		GetSession: func(context.Context, int32) (ManagedSessionDetails, error) {
			return ManagedSessionDetails{}, nil
		},
		AskAgent: func(context.Context, int32, string) (string, error) { return "", nil },
		RunNewSession: func(_ context.Context, source *int32, agent, prompt string) (string, error) {
			gotSource, gotAgent, gotPrompt = source, agent, prompt
			return "queued", nil
		},
		StopSession: func(context.Context, int32, string) (string, error) { return "", nil },
		ForkSession: func(context.Context, int32, int64) (string, error) { return "", nil },
	})
	_, err := r.Execute(context.Background(), string(OrchestratorToolRunNewSession), json.RawMessage(`{"source_session_id":4,"agent_name":"Coder","prompt":"Implement the parser and add tests"}`))
	require.NoError(t, err)
	require.Equal(t, int32(4), *gotSource)
	require.Equal(t, "Coder", gotAgent)
	require.Equal(t, "Implement the parser and add tests", gotPrompt)
	_, err = r.Execute(context.Background(), string(OrchestratorToolRunNewSession), json.RawMessage(`{"agent_name":"QA","prompt":"Run the regression suite"}`))
	require.NoError(t, err)
	require.Nil(t, gotSource)
	require.Equal(t, "QA", gotAgent)
	require.Equal(t, "Run the regression suite", gotPrompt)
	require.Error(t, func() error {
		_, err := r.Execute(context.Background(), string(OrchestratorToolRunNewSession), json.RawMessage(`{"agent_name":"","prompt":"x"}`))
		return err
	}())
	require.Error(t, func() error {
		_, err := r.Execute(context.Background(), string(OrchestratorToolRunNewSession), json.RawMessage(`{"agent_name":"Coder","prompt":""}`))
		return err
	}())
	require.Error(t, func() error {
		_, err := r.Execute(context.Background(), string(OrchestratorToolRunNewSession), json.RawMessage(`{"agent_name":"  ","prompt":"do work"}`))
		return err
	}())
}

func TestForkSessionValidatesCanonicalMessageID(t *testing.T) {
	called := false
	r := NewOrchestratorRegistry(OrchestratorCallbacks{
		GetSessionList: func(context.Context) ([]ManagedSessionSummary, error) { return nil, nil },
		GetSession: func(context.Context, int32) (ManagedSessionDetails, error) {
			return ManagedSessionDetails{}, nil
		},
		AskAgent:      func(context.Context, int32, string) (string, error) { return "", nil },
		RunNewSession: func(context.Context, *int32, string, string) (string, error) { return "", nil },
		StopSession:   func(context.Context, int32, string) (string, error) { return "", nil },
		ForkSession: func(_ context.Context, sessionID int32, messageID int64) (string, error) {
			called = sessionID == 7 && messageID == 42
			return "forked", nil
		},
	})
	_, err := r.Execute(context.Background(), string(OrchestratorToolForkSession), json.RawMessage(`{"session_id":7,"fork_message_id":0}`))
	require.Error(t, err)
	require.False(t, called)
	result, err := r.Execute(context.Background(), string(OrchestratorToolForkSession), json.RawMessage(`{"session_id":7,"fork_message_id":42}`))
	require.NoError(t, err)
	require.Equal(t, "forked", result)
	require.True(t, called)
}
