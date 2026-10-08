package engine

import (
	"context"
	"sync"
	"sync/atomic"
)

// runRegistry owns the process-local resources of running executor sessions.
// Durable state lives in the database; this holds only cancellation handles
// and the bookkeeping for a graceful shutdown.
type runRegistry struct {
	cancelFuncs sync.Map // runID -> context.CancelFunc
	draining    atomic.Bool
	active      sync.WaitGroup
}

func newRunRegistry() *runRegistry { return &runRegistry{} }

func (registry *runRegistry) beginDrain() { registry.draining.Store(true) }

func (registry *runRegistry) waitForActiveRuns(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		registry.active.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
