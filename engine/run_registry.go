package engine

import (
	"context"
	"sync"
	"sync/atomic"
)

// runRegistry owns the process-local resources associated with active runs.
// Durable lifecycle state remains in the database; this registry contains only
// cancellation handles, synchronous question brokers, and drain bookkeeping.
type runRegistry struct {
	cancelFuncs                 sync.Map // runID -> context.CancelFunc
	sessionQuestionBrokers      sync.Map // runID -> *sessionQuestionBroker
	orchestratorQuestionBrokers sync.Map // runID -> *orchestratorQuestionBroker
	draining                    atomic.Bool
	activeRoots                 sync.WaitGroup
}

func newRunRegistry() *runRegistry { return &runRegistry{} }

func (registry *runRegistry) beginDrain() {
	registry.draining.Store(true)
}

func (registry *runRegistry) waitForActiveRoots(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		registry.activeRoots.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
