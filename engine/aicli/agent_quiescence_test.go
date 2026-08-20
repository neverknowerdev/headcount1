package aicli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// RunWithHistory must not return while a bookkeeping write launched by the
// session is still in flight. This is the boundary that lets the E2E reset
// stop a run, wait for the engine, and then safely delete its database rows.
func TestRunWithHistoryWaitsForAsyncPersistence(t *testing.T) {
	var released atomic.Bool
	a := &Agent{Mode: Mode("unsupported")}
	a.asyncPersistence.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	finished := make(chan struct{})
	go func() {
		_, _, _ = a.RunWithHistory(ctx, nil, nil)
		released.Store(true)
		close(finished)
	}()

	select {
	case <-finished:
		t.Fatal("RunWithHistory returned before async persistence drained")
	case <-time.After(25 * time.Millisecond):
	}
	require.False(t, released.Load())

	a.asyncPersistence.Done()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("RunWithHistory did not return after async persistence drained")
	}
}
