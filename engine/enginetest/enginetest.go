// Package enginetest provides a stand-in for the engine, for tests of the
// layers above it that only need to know what they asked of it.
package enginetest

import (
	"context"
	"fmt"
	"sync"

	"agent-orchestrator/engine"
)

// Recorder is an Engine that does nothing but remember its calls, each as
// "method arguments" (for example "rerun 3" or "status 3 done"), and return
// the error configured for that method.
type Recorder struct {
	mu    sync.Mutex
	calls []string

	RerunErr      error
	StopErr       error
	StatusErr     error
	HumanReplyErr error
}

var _ engine.Engine = (*Recorder)(nil)

func (r *Recorder) record(format string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf(format, args...))
}

// Calls returns what has been asked of the engine so far, in order.
func (r *Recorder) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *Recorder) ProcessTask(_ context.Context, taskID int32) error {
	r.record("process %d", taskID)
	return nil
}

func (r *Recorder) RerunTask(_ context.Context, taskID int32) error {
	r.record("rerun %d", taskID)
	return r.RerunErr
}

func (r *Recorder) StopTask(_ context.Context, taskID int32) error {
	r.record("stop %d", taskID)
	return r.StopErr
}

func (r *Recorder) SetTaskStatus(_ context.Context, taskID int32, status string) error {
	r.record("status %d %s", taskID, status)
	return r.StatusErr
}

func (r *Recorder) StopRun(_ context.Context, runID int32) { r.record("stop-run %d", runID) }

func (r *Recorder) HandleHumanReply(_ context.Context, taskID int32) error {
	r.record("human-reply %d", taskID)
	return r.HumanReplyErr
}

func (r *Recorder) NotifyCredentialsChanged() { r.record("credentials-changed") }
