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
		GetSessions:      func(context.Context) ([]OrchestratorSession, error) { return nil, nil },
		GetSessionStatus: func(context.Context, int32) (OrchestratorSession, error) { return OrchestratorSession{}, nil },
		AskTaskOwner:     func(context.Context, int32, string) (string, error) { return "", nil },
		RunNewSession:    func(context.Context, *int32, string) (string, error) { return "", nil },
		StopSession:      func(context.Context, int32, string) (string, error) { return "", nil },
		ForkSession:      func(context.Context, int32, int64) (string, error) { return "", nil },
	})
	require.Equal(t, []string{"ask_task_owner", "fork_session", "get_session_status", "get_sessions", "run_new_session", "stop_session"}, r.Names())
	require.Error(t, func() error { _, err := r.Execute(context.Background(), string(aicli.ToolWrite), nil); return err }())
}

func TestOrchestratorToolValidation(t *testing.T) {
	called := false
	r := NewOrchestratorRegistry(OrchestratorCallbacks{
		GetSessions:      func(context.Context) ([]OrchestratorSession, error) { return nil, nil },
		GetSessionStatus: func(context.Context, int32) (OrchestratorSession, error) { return OrchestratorSession{}, nil },
		AskTaskOwner:     func(context.Context, int32, string) (string, error) { called = true; return "ok", nil },
		RunNewSession:    func(context.Context, *int32, string) (string, error) { return "", nil },
		StopSession:      func(context.Context, int32, string) (string, error) { return "", nil },
		ForkSession:      func(context.Context, int32, int64) (string, error) { return "", nil },
	})
	_, err := r.Execute(context.Background(), string(OrchestratorToolAskTaskOwner), json.RawMessage(`{"session_id":0,"question":"x"}`))
	require.Error(t, err)
	require.False(t, called)
	_, err = r.Execute(context.Background(), string(OrchestratorToolAskTaskOwner), json.RawMessage(`{"session_id":3,"question":"status?"}`))
	require.NoError(t, err)
	require.True(t, called)
}
