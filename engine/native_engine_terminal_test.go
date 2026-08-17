package engine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
