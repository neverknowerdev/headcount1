package engine

import (
	"agent-orchestrator/engine/aicli"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRebaseForkHistoryRuntimeMetadata(t *testing.T) {
	history := []aicli.Message{{
		Role:    "system",
		Content: "task context\nWorkdir: /old/workdir\nRuntime session ID: 7",
	}, {Role: "user", Content: "continue"}}

	rebased := rebaseForkHistoryRuntimeMetadata(history, 11, "/new/workdir")
	require.Len(t, rebased, 2)
	require.Contains(t, rebased[0].Content, "Workdir: /new/workdir")
	require.Contains(t, rebased[0].Content, "Runtime session ID: 11")
	require.NotContains(t, rebased[0].Content, "/old/workdir")
	assert.Equal(t, "continue", rebased[1].Content)
	assert.Equal(t, history[0].Content, "task context\nWorkdir: /old/workdir\nRuntime session ID: 7")
}

func TestSessionFinishedUsesWorkerTerminalFlag(t *testing.T) {
	tests := []struct {
		name   string
		worker bool
		state  sessionToolState
		want   bool
	}{
		{name: "root finish task", state: sessionToolState{taskFinished: true}, want: true},
		{name: "root ignores worker flag", state: sessionToolState{workerFinished: true}, want: false},
		{name: "worker finish work", worker: true, state: sessionToolState{workerFinished: true}, want: true},
		{name: "worker ignores finish task", worker: true, state: sessionToolState{taskFinished: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sessionFinished(sessionOptions{Worker: tt.worker}, &tt.state)
			require.Equal(t, tt.want, got)
		})
	}
}
