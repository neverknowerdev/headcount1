package engine

import (
	"context"
	"fmt"
	"time"
)

const (
	defaultSweepInterval = 30 * time.Second
	// driverWorkers is how many tasks are advanced at once. A worker is busy
	// for the length of a smart call, so this bounds concurrent smart calls.
	driverWorkers = 8
)

// sweep asks for every task the workflow is responsible for and nobody is
// holding. It is what makes a lost wake-up harmless: whatever event failed to
// arrive, the task is evaluated again here and continues from its stored
// state. Looking at a task that needs nothing costs one read.
func (d *workflowDriver) sweep(ctx context.Context) {
	for _, id := range d.reapSessions(ctx) {
		d.Enqueue(id)
	}
	ids, err := d.q.ListTasksToAdvance(ctx, 0)
	if err != nil {
		fmt.Printf("Warning: workflow sweep could not list tasks: %v\n", err)
		return
	}
	for _, id := range ids {
		d.Enqueue(id)
	}
}

// Start runs the driver until ctx ends: its workers, and the sweeper, which
// also runs once immediately so tasks interrupted by a restart continue.
func (d *workflowDriver) Start(ctx context.Context) {
	for i := 0; i < driverWorkers; i++ {
		go d.work(ctx)
	}
	go func() {
		d.sweep(ctx)
		ticker := time.NewTicker(d.sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.sweep(ctx)
			}
		}
	}()
}
