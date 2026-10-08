package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/workflow"
	"agent-orchestrator/eventhub"
)

const (
	// taskLeaseTTL is how long a driver's hold on a task lasts without being
	// renewed. A step in progress renews it every third of that; after a crash
	// it is how long the task waits before another pass may redo the step.
	taskLeaseTTL = 45 * time.Second
	// maxTransitionsPerAdvance bounds one pass over a task. The workflow's own
	// budgets end every loop; this only stops a defect from spinning.
	maxTransitionsPerAdvance = 64
	// executorSlotRetry is how soon a task that found no free executor slot
	// is looked at again.
	executorSlotRetry = 5 * time.Second
)

// workflowDriver moves tasks through the workflow. For each task it is asked
// about, it reads the task's state from the database, asks the workflow
// package what should happen, does that one thing, and commits the result —
// repeating until the task is waiting on something or finished.
//
// The driver holds no task state of its own. Everything it knows comes from
// the database on every pass, so a pass that is interrupted at any point, or
// a wake-up that never arrives, is repaired by simply looking at the task
// again: that is what the sweeper does.
type workflowDriver struct {
	q         *db.Queries
	hub       *eventhub.Hub
	budgets   workflow.Budgets
	newClient clientFactory
	// basePath is the data root the journal is mirrored under.
	basePath func() string
	now      func() time.Time
	// startExecutor launches the session a direct task was just put to wait
	// on. A slot is reserved before the start is committed and released if it
	// is not; the session gives its slot back when it ends.
	startExecutor       func(task db.Task, run db.Run)
	reserveExecutorSlot func() bool
	releaseExecutorSlot func()
	// reapSessions fails executor sessions that died without saying so, and
	// returns the tasks that were waiting on them.
	reapSessions func(ctx context.Context) []int32
	// publishPR opens or updates a root task's pull request after its
	// verification passed.
	publishPR func(task db.Task)
	// stopSessions ends the executor sessions of the given tasks.
	stopSessions func(ctx context.Context, taskIDs []int32)

	// advancing holds the cancel function of each task's pass in progress, so
	// a stop can interrupt a slow model call instead of waiting for it.
	advancing sync.Map

	// sweepInterval is how often every unfinished, unheld task is looked at
	// again whether or not anything asked for it.
	sweepInterval time.Duration

	instance string

	mu      sync.Mutex
	pending map[int32]*queueState
	queue   []int32
	wake    chan struct{}
}

// queueState tracks one task in the in-memory queue: whether it is waiting
// its turn, being advanced, and whether it was asked for again meanwhile.
type queueState struct {
	queued  bool
	running bool
	again   bool
}

func newWorkflowDriver(q *db.Queries, hub *eventhub.Hub) *workflowDriver {
	nonce := make([]byte, 6)
	_, _ = rand.Read(nonce)
	return &workflowDriver{
		q:                   q,
		hub:                 hub,
		budgets:             workflow.DefaultBudgets,
		newClient:           aicli.NewClient,
		basePath:            func() string { return loadSettings().BasePath },
		now:                 time.Now,
		startExecutor:       func(db.Task, db.Run) {},
		reserveExecutorSlot: func() bool { return true },
		releaseExecutorSlot: func() {},
		reapSessions:        func(context.Context) []int32 { return nil },
		publishPR:           func(db.Task) {},
		stopSessions:        func(context.Context, []int32) {},
		sweepInterval:       defaultSweepInterval,
		instance:            "driver-" + hex.EncodeToString(nonce),
		pending:             map[int32]*queueState{},
		wake:                make(chan struct{}, 1),
	}
}

// Enqueue asks for a task to be looked at. It is always safe to call: asking
// about a task that needs nothing does nothing, and asking twice coalesces.
func (d *workflowDriver) Enqueue(taskID int32) {
	d.mu.Lock()
	state := d.pending[taskID]
	if state == nil {
		state = &queueState{}
		d.pending[taskID] = state
	}
	switch {
	case state.running:
		// Something changed while the task was being advanced; look again
		// when that pass ends, since it may have read the older state.
		state.again = true
	case !state.queued:
		state.queued = true
		d.queue = append(d.queue, taskID)
	}
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// EnqueueAfter asks for a task to be looked at once a delay has passed.
func (d *workflowDriver) EnqueueAfter(taskID int32, delay time.Duration) {
	if delay <= 0 {
		d.Enqueue(taskID)
		return
	}
	time.AfterFunc(delay, func() { d.Enqueue(taskID) })
}

// take removes the next queued task, marking it running.
func (d *workflowDriver) take() (int32, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.queue) == 0 {
		return 0, false
	}
	taskID := d.queue[0]
	d.queue = d.queue[1:]
	state := d.pending[taskID]
	state.queued, state.running = false, true
	return taskID, true
}

// finish marks a task's pass over; if it was asked for again meanwhile it
// goes back in the queue.
func (d *workflowDriver) finish(taskID int32) {
	d.mu.Lock()
	state := d.pending[taskID]
	state.running = false
	requeue := state.again
	state.again = false
	if requeue {
		state.queued = true
		d.queue = append(d.queue, taskID)
	} else {
		delete(d.pending, taskID)
	}
	d.mu.Unlock()
	if requeue {
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}
}

// work runs one queue worker until ctx ends.
func (d *workflowDriver) work(ctx context.Context) {
	for {
		taskID, ok := d.take()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-d.wake:
				continue
			}
		}
		d.advance(ctx, taskID)
		d.finish(taskID)
		// Another worker may be idle while more work is queued.
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}
}

// advance moves one task as far as it can go right now.
func (d *workflowDriver) advance(ctx context.Context, taskID int32) {
	owner := fmt.Sprintf("%s/%d", d.instance, d.now().UnixNano())
	held, err := d.q.AcquireTaskLease(ctx, taskID, owner, taskLeaseTTL)
	if err != nil {
		fmt.Printf("Warning: workflow could not lease task %d: %v\n", taskID, err)
		return
	}
	if !held {
		// Another driver has it. If that driver dies, its lease expires and
		// the sweeper brings the task back here.
		return
	}
	defer func() {
		if err := d.q.ReleaseTaskLease(context.Background(), taskID, owner); err != nil {
			fmt.Printf("Warning: workflow could not release task %d: %v\n", taskID, err)
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	d.advancing.Store(taskID, cancel)
	defer func() {
		d.advancing.Delete(taskID)
		cancel()
	}()

	for pass := 0; pass < maxTransitionsPerAdvance; pass++ {
		if ctx.Err() != nil {
			return
		}
		loaded, err := d.load(ctx, taskID)
		if err != nil {
			fmt.Printf("Warning: workflow could not read task %d: %v\n", taskID, err)
			return
		}
		next := workflow.Evaluate(loaded.snapshot, d.budgets)

		var transition *workflow.Transition
		var extras applyExtras
		switch next.Kind {
		case workflow.Idle:
			return
		case workflow.Apply:
			transition = next.Transition
			extras.answer = loaded.answer
		case workflow.SmartStep:
			transition, extras, err = d.smartStep(ctx, loaded, next, owner)
			if err != nil {
				// The pass was interrupted (shutdown, lease lost). Nothing was
				// committed for this step; the task is exactly as it was.
				return
			}
		}

		reserved := false
		if transition.StartRun {
			if !d.reserveExecutorSlot() {
				// Every executor is busy; the task stays ready and is looked
				// at again shortly.
				d.EnqueueAfter(taskID, executorSlotRetry)
				return
			}
			reserved = true
		}
		committed, err := d.apply(ctx, loaded, transition, extras, owner)
		if err != nil && reserved {
			d.releaseExecutorSlot()
		}
		switch {
		case errors.Is(err, db.ErrTransitionConflict), errors.Is(err, errWorkspaceBusy):
			// The task changed under us; read it again and decide afresh.
			extras.recordUsage(nil)
			continue
		case err != nil:
			extras.recordUsage(nil)
			if !errors.Is(err, db.ErrLeaseLost) {
				fmt.Printf("Warning: workflow could not apply a step to task %d: %v\n", taskID, err)
			}
			return
		}
		d.afterCommit(ctx, committed, extras)
	}
	// Still not settled after many transitions: yield and come back.
	d.EnqueueAfter(taskID, time.Second)
}

// keepLease renews the task's lease while a slow call is in flight. It
// returns a context that is canceled if the lease is lost, and a function
// that stops the renewal.
func (d *workflowDriver) keepLease(ctx context.Context, taskID int32, owner string) (context.Context, func()) {
	callCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(taskLeaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-callCtx.Done():
				return
			case <-ticker.C:
				held, err := d.q.RenewTaskLease(context.Background(), taskID, owner, taskLeaseTTL)
				if err == nil && !held {
					cancel()
					return
				}
			}
		}
	}()
	return callCtx, func() {
		close(done)
		cancel()
	}
}

func (d *workflowDriver) broadcast(companyID int32, event string, payload interface{}) {
	if d.hub != nil {
		d.hub.BroadcastEventForCompany(companyID, event, payload)
	}
}
