package engine

import (
	"context"
	"sync"
	"sync/atomic"
)

// runRegistry owns the process-local resources associated with active runs.
// Durable lifecycle state remains in the database; this registry contains only
// cancellation handles and drain bookkeeping.
type runRegistry struct {
	cancelFuncs sync.Map // runID -> context.CancelFunc
	// orchestrators contains the process-local ownership of the sidecar loop.
	// Durable run state is still authoritative; this guard only prevents a
	// watchdog tick or a restart reconciliation from starting two loops for
	// the same orchestrator row.
	orchestrators sync.Map // runID -> struct{}
	draining      atomic.Bool
	drainDone     chan struct{}
	drainOnce     sync.Once
	activeRoots   sync.WaitGroup
}

func newRunRegistry() *runRegistry { return &runRegistry{drainDone: make(chan struct{})} }

func (registry *runRegistry) beginDrain() {
	registry.draining.Store(true)
	registry.drainOnce.Do(func() { close(registry.drainDone) })
}

// contextWithDrain returns a context for a passive sidecar that must stop
// promptly when the process begins a graceful restart. Regular agent runs use
// their pause callback instead: they need the in-flight provider response in
// order to persist a safe checkpoint. Orchestrators have no such checkpoint,
// so canceling their current provider call is the correct restart behavior.
func (registry *runRegistry) contextWithDrain(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-registry.drainDone:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
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

func (registry *runRegistry) tryStartOrchestrator(runID int32) bool {
	_, loaded := registry.orchestrators.LoadOrStore(runID, struct{}{})
	return !loaded
}

func (registry *runRegistry) stopOrchestrator(runID int32) {
	registry.orchestrators.Delete(runID)
}
