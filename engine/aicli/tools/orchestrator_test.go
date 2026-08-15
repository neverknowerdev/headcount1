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
		GetSessions: func(context.Context) ([]ManagedSessionSummary, error) { return nil, nil },
		GetSessionLastRunStatus: func(context.Context, int32) (ManagedSessionStatusReport, error) {
			return ManagedSessionStatusReport{}, nil
		},
		AskSessionAgent: func(context.Context, int32, string) (string, error) { return "", nil },
		RunNewSession:   func(context.Context, *int32, *int32, string, bool) (string, error) { return "", nil },
		StopSession:     func(context.Context, int32, string) (string, error) { return "", nil },
		ForkSession:     func(context.Context, int32, int64) (string, error) { return "", nil },
	})
	require.Equal(t, []string{"ask_session_agent", "fork_session", "get_session_last_run_status", "get_sessions", "run_new_session", "stop_session"}, r.Names())
	require.Error(t, func() error { _, err := r.Execute(context.Background(), string(aicli.ToolWrite), nil); return err }())
}

func TestOrchestratorToolValidation(t *testing.T) {
	called := false
	r := NewOrchestratorRegistry(OrchestratorCallbacks{
		GetSessions: func(context.Context) ([]ManagedSessionSummary, error) { return nil, nil },
		GetSessionLastRunStatus: func(context.Context, int32) (ManagedSessionStatusReport, error) {
			return ManagedSessionStatusReport{}, nil
		},
		AskSessionAgent: func(context.Context, int32, string) (string, error) { called = true; return "ok", nil },
		RunNewSession:   func(context.Context, *int32, *int32, string, bool) (string, error) { return "", nil },
		StopSession:     func(context.Context, int32, string) (string, error) { return "", nil },
		ForkSession:     func(context.Context, int32, int64) (string, error) { return "", nil },
	})
	_, err := r.Execute(context.Background(), string(OrchestratorToolAskSessionAgent), json.RawMessage(`{"session_id":0,"question":"x"}`))
	require.Error(t, err)
	require.False(t, called)
	_, err = r.Execute(context.Background(), string(OrchestratorToolAskSessionAgent), json.RawMessage(`{"session_id":3,"question":"status?"}`))
	require.NoError(t, err)
	require.True(t, called)
}

func TestRunNewSessionPassesOptionalContextAndTargetArguments(t *testing.T) {
	var gotSource, gotAgent *int32
	var gotReason string
	var gotContext bool
	r := NewOrchestratorRegistry(OrchestratorCallbacks{
		GetSessions: func(context.Context) ([]ManagedSessionSummary, error) { return nil, nil },
		GetSessionLastRunStatus: func(context.Context, int32) (ManagedSessionStatusReport, error) {
			return ManagedSessionStatusReport{}, nil
		},
		AskSessionAgent: func(context.Context, int32, string) (string, error) { return "", nil },
		RunNewSession: func(_ context.Context, source, agent *int32, reason string, include bool) (string, error) {
			gotSource, gotAgent, gotReason, gotContext = source, agent, reason, include
			return "queued", nil
		},
		StopSession: func(context.Context, int32, string) (string, error) { return "", nil },
		ForkSession: func(context.Context, int32, int64) (string, error) { return "", nil },
	})
	_, err := r.Execute(context.Background(), string(OrchestratorToolRunNewSession), json.RawMessage(`{"source_session_id":4,"agent_id":9,"reason":"difficult question","include_task_context":false}`))
	require.NoError(t, err)
	require.Equal(t, int32(4), *gotSource)
	require.Equal(t, int32(9), *gotAgent)
	require.Equal(t, "difficult question", gotReason)
	require.False(t, gotContext)
	_, err = r.Execute(context.Background(), string(OrchestratorToolRunNewSession), json.RawMessage(`{"reason":"small action"}`))
	require.NoError(t, err)
	require.Nil(t, gotSource)
	require.Nil(t, gotAgent)
	require.True(t, gotContext)
	_, err = r.Execute(context.Background(), string(OrchestratorToolRunNewSession), json.RawMessage(`{"reason":"research"}`))
	require.NoError(t, err)
	require.Equal(t, "research", gotReason)
	require.Error(t, func() error {
		_, err := r.Execute(context.Background(), string(OrchestratorToolRunNewSession), json.RawMessage(`{"agent_id":0,"reason":"x"}`))
		return err
	}())
}

func TestForkSessionValidatesCanonicalMessageID(t *testing.T) {
	called := false
	r := NewOrchestratorRegistry(OrchestratorCallbacks{
		GetSessions: func(context.Context) ([]ManagedSessionSummary, error) { return nil, nil },
		GetSessionLastRunStatus: func(context.Context, int32) (ManagedSessionStatusReport, error) {
			return ManagedSessionStatusReport{}, nil
		},
		AskSessionAgent: func(context.Context, int32, string) (string, error) { return "", nil },
		RunNewSession:   func(context.Context, *int32, *int32, string, bool) (string, error) { return "", nil },
		StopSession:     func(context.Context, int32, string) (string, error) { return "", nil },
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
