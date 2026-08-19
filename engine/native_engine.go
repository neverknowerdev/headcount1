package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/git"
	"agent-orchestrator/pkg/githubapp"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"

	"gorm.io/gorm"
)

const runStatusCompleted = "completed"

const (
	executionModeImplementation = "implementation"
	executionModeRefinement     = "refinement" // refinement (planning)
	sessionModeImplement        = "implement"
	sessionModePlan             = "plan"
)

// ResumeCause identifies why a persisted session is being continued. The
// update path uses only ResumeAfterUpdate automatically; failed and stale
// recovery are deliberately explicit callers for now.
type ResumeCause string

const (
	ResumeAfterUpdate  ResumeCause = "binary_update"
	ResumeAfterFailure ResumeCause = "failed_recovery"
	ResumeAfterStale   ResumeCause = "stale_recovery"
	ResumeAfterHuman   ResumeCause = "human_input"
)

func recoveryReason(run db.Run) string {
	if run.Recovery.RecoveryReason != "" {
		return run.Recovery.RecoveryReason
	}
	if run.Status == db.RunStatusRecoverableFailed || run.Status == "failed" {
		return "a previous failure was explicitly recovered"
	}
	if run.Status == db.RunStatusStale {
		return "a stale session was explicitly recovered"
	}
	return "a planned server restart"
}

type ResumeOptions struct {
	Cause       ResumeCause
	InitiatorID *int32
	Reason      string
	TargetBuild string
}

// sessionOptions controls orchestrator-created auxiliary sessions without
// adding persistence-only columns to Run. SeedHistory is used by forks; the
// task-context flag controls whether a fresh session receives the task prompt.
type sessionOptions struct {
	// SeedHistory is the source conversation for a fork. The source workspace
	// is copied before the new session starts, so tool calls are never replayed.
	SeedHistory        []aicli.Message
	Instruction        string
	IncludeTaskContext bool
	SkipTaskLock       bool
	// PrecreatedRun is used by fork_session so the caller can return the new
	// run ID synchronously while executeSession still owns normal setup and
	// terminal-state cleanup.
	PrecreatedRun      *db.Run
	Worker             bool
	WorkerWorkspace    string
	WorkerReadOnlyDirs []string
	WorkerProvider     db.LLMProvider
	WorkerModel        string
	Consultation       bool
}

// sessionFinished uses the terminal flag owned by the session's tool set.
// Helper workers intentionally expose finish_work rather than finish_task, so
// their completion must not be judged by the root-session flag.
func sessionFinished(options sessionOptions, state *sessionToolState) bool {
	if options.Worker {
		return state.workerFinished
	}
	return state.taskFinished
}

// NativeEngine implements Engine using the aicli package for direct LLM communication.
type NativeEngine struct {
	q    *db.Queries
	hub  *eventhub.Hub
	runs *runRegistry
}

// NewNativeEngine creates a NativeEngine. Agent rows in the database contain
// the complete runtime configuration; file-backed role definitions are used
// only by the legacy/bootstrap path in agent_runtime.go.
func NewNativeEngine(database *gorm.DB, hub *eventhub.Hub) *NativeEngine {
	return &NativeEngine{
		q:    db.New(database),
		hub:  hub,
		runs: newRunRegistry(),
	}
}

// CheckStaleRuns retires running sessions whose heartbeat has exceeded the
// supplied threshold. It is deliberately independent from startup recovery so
// a live server can repair a wedged session without waiting for a reload.
func (e *NativeEngine) CheckStaleRuns(ctx context.Context, threshold time.Duration) ([]int32, error) {
	runs, err := e.q.GetStaleRunningRuns(ctx, threshold)
	if err != nil {
		return nil, err
	}
	// A row can also be stale even with a recently-written heartbeat when the
	// goroutine that owned it vanished and another code path refreshed the row.
	// Cross-check the in-memory ownership map once per monitor tick; this is a
	// cheap second line of defence against orphaned "running" rows.
	activeRuns, activeErr := e.q.GetRunningRuns(ctx)
	if activeErr != nil {
		return nil, activeErr
	}
	known := make(map[int32]bool, len(runs))
	for _, run := range runs {
		known[run.ID] = true
	}
	cutoff := time.Now().Add(-threshold)
	for _, run := range activeRuns {
		if _, owned := e.runs.cancelFuncs.Load(run.ID); owned || known[run.ID] {
			continue
		}
		last := run.StartedAt
		if run.LastMessageTime != nil {
			last = *run.LastMessageTime
		}
		if last.Before(cutoff) {
			runs = append(runs, run)
			known[run.ID] = true
		}
	}
	stale := make([]int32, 0, len(runs))
	for _, run := range runs {
		changed, markErr := e.q.MarkRunStale(ctx, run.ID, "session stopped heartbeating")
		if markErr != nil {
			return stale, markErr
		}
		if !changed {
			continue
		}
		stale = append(stale, run.ID)
		e.broadcastForTask(ctx, run.TaskID, "run_ended", map[string]interface{}{"run_id": run.ID, "status": db.RunStatusStale})
	}
	return stale, nil
}

// WakeStalledOrchestrators emits one durable recovery event when every
// managed worker is inactive and at least one worker is stale. The monitor
// performs this reconciliation; the orchestrator model never has to poll or
// infer that a silent session needs attention. Human-gated tasks are an
// explicit exception: silence is expected while the user is deciding.
func (e *NativeEngine) WakeStalledOrchestrators(ctx context.Context, staleIDs []int32) error {
	if len(staleIDs) == 0 {
		return nil
	}
	staleSet := make(map[int32]struct{}, len(staleIDs))
	for _, id := range staleIDs {
		staleSet[id] = struct{}{}
	}
	orchestrators, err := e.q.ListWaitingOrchestrators(ctx)
	if err != nil {
		return err
	}
	for _, orchestrator := range orchestrators {
		task, taskErr := e.q.GetTask(ctx, orchestrator.TaskID)
		if taskErr != nil || isTerminalTaskStatus(task.Status) || e.humanInputPending(ctx, task.ID) {
			continue
		}
		sessions, listErr := e.q.ListOrchestratorSessions(ctx, orchestrator.ID)
		if listErr != nil {
			return listErr
		}
		allInactive := true
		hasStale := false
		var source int32
		for _, session := range sessions {
			switch session.Status {
			case "running", "waiting", db.RunStatusResuming:
				allInactive = false
			}
			if _, marked := staleSet[session.ID]; marked || session.Status == db.RunStatusStale {
				hasStale = true
				if source == 0 {
					source = session.ID
				}
			}
		}
		if !allInactive || !hasStale || source == 0 {
			continue
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"kind": "watchdog_recovery", "task_id": task.ID, "stale_run_ids": staleIDs,
			"message": "All managed sessions are inactive and at least one session is stale; reconcile the worker tree.",
		})
		if _, enqueueErr := e.q.EnqueueRoutedEvent(ctx, task.ID, source, orchestrator.ID,
			db.RunEventTypeLifecycleStatus, string(payload), fmt.Sprintf("watchdog:%d:%d", orchestrator.ID, source)); enqueueErr != nil {
			return enqueueErr
		}
	}
	return nil
}

// StartLivenessMonitor runs the stale-session check on a bounded cadence. The
// context owns the goroutine, making it safe to stop during server shutdown.
func (e *NativeEngine) StartLivenessMonitor(ctx context.Context, interval, staleAfter time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stale, err := e.CheckStaleRuns(context.Background(), staleAfter)
				if err != nil {
					fmt.Printf("Warning: stale-session monitor failed: %v\n", err)
					continue
				}
				if err := e.WakeStalledOrchestrators(context.Background(), stale); err != nil {
					fmt.Printf("Warning: orchestrator watchdog failed: %v\n", err)
				}
			}
		}
	}()
}

// BeginDrain flips the engine into drain mode for a graceful shutdown (e.g.
// applying an auto-update): processTask stops starting new runs, and every
// active root run is asked to pause at its next safe turn boundary — right
// after its current in-flight LLM call returns — instead of continuing.
// Idempotent; safe to call more than once.
func (e *NativeEngine) BeginDrain() {
	e.runs.beginDrain()
}

// WaitForActiveRuns blocks until every active root run has either finished
// normally or paused (see BeginDrain), or ctx's deadline passes — whichever
// comes first. Runs still active when the deadline passes are abandoned (the
// process is about to exit); they are recovered as ordinary crashed runs by
// the existing stale-run cleanup on the next boot, exactly as an ungraceful
// kill -9 would leave them today.
func (e *NativeEngine) WaitForActiveRuns(ctx context.Context) {
	e.runs.waitForActiveRoots(ctx)
}

// parentSession carries durable run-tree context for an auxiliary session.
// Coordination itself is handled by persisted RunEvents and the task
// orchestrator; this is execution metadata, not an in-process channel.
type parentSession struct {
	parentRunID int32
	rootRunID   int32
	rootTaskID  int32
}

// ProcessTask reacts to a task's current status and spawns a goroutine to run
// the agent when that status implies pending work. A queued task with
// unfinished hard dependencies is moved to depends-on-task and is not run.
func (e *NativeEngine) ProcessTask(ctx context.Context, taskID int32) error {
	return e.processTask(ctx, taskID, false)
}

// RerunTask forces a new agent run for an explicit user action (the Re-run
// button or a comment with the Run Agent flag), moving the task from a
// terminal status back to "in-progress" if necessary.
func (e *NativeEngine) RerunTask(ctx context.Context, taskID int32) error {
	return e.processTask(ctx, taskID, true)
}

func (e *NativeEngine) processTask(ctx context.Context, taskID int32, forceRerun bool) error {
	// Draining for a graceful shutdown (see BeginDrain): refuse to start any
	// new run. Existing runs already in flight are left to reach their own
	// pause point; this task will be picked up again on the next boot, either
	// via its resumed run or (once that completes) the next natural status
	// transition.
	if e.runs.draining.Load() {
		return nil
	}

	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	// Deduplication: skip if a non-stale run is already active.
	if task.RunID != nil {
		isStale, err := e.q.IsRunStale(ctx, *task.RunID, 5*time.Minute)
		if err != nil {
			return fmt.Errorf("failed to check run staleness: %w", err)
		}
		if !isStale {
			return nil
		}
		fmt.Printf("Task %d has stale run %d, resolving before new run\n", taskID, *task.RunID)
		e.resolveStaleRun(ctx, *task.RunID)
	}

	switch task.Status {
	case db.TaskStatusTodo:
		return e.startQueuedTask(ctx, task, forceRerun)
	case db.TaskStatusRefinement:
		// Refinement is a planning-only execution mode. It returns the task to
		// to-do after a valid specification handoff.
		go e.run(context.Background(), task, sessionModePlan)
	case db.TaskStatusDependsOnTask:
		ready, blockers, err := e.q.CanStartTask(ctx, task.ID)
		if err != nil {
			return err
		}
		if !ready {
			if forceRerun {
				return &TaskDependencyBlockedError{TaskID: task.ID, Blockers: blockers}
			}
			return nil
		}
		prevStatus := task.Status
		task.Status = db.TaskStatusTodo
		if _, err := e.q.UpdateTask(ctx, task); err != nil {
			return err
		}
		e.broadcastTaskStatus(task, prevStatus, task.Status, blockers)
		return e.processTask(ctx, task.ID, forceRerun)
	case db.TaskStatusInProgress:
		go e.run(context.Background(), task, sessionModeImplement)
	case db.TaskStatusInReview, db.TaskStatusBlocked, db.TaskStatusDone:
		// Only an explicit re-run (Re-run button, Run Agent comment) may pull
		// a task out of these statuses; a plain status change never does.
		if !forceRerun {
			return nil
		}
		ready, blockers, err := e.q.CanStartTask(ctx, task.ID)
		if err != nil {
			return err
		}
		if !ready {
			if task.Status != db.TaskStatusDependsOnTask {
				prevStatus := task.Status
				task.Status = db.TaskStatusDependsOnTask
				if _, err := e.q.UpdateTask(ctx, task); err != nil {
					return err
				}
				e.broadcastTaskStatus(task, prevStatus, task.Status, blockers)
			}
			if forceRerun {
				return &TaskDependencyBlockedError{TaskID: task.ID, Blockers: blockers}
			}
			return nil
		}
		prevStatus := task.Status
		task.Status = db.TaskStatusInProgress
		if _, err := e.q.UpdateTask(ctx, task); err != nil {
			return err
		}
		e.broadcastTaskStatus(task, prevStatus, task.Status, nil)
		go e.run(context.Background(), task, sessionModeImplement)
	}

	return nil
}

func (e *NativeEngine) startQueuedTask(ctx context.Context, task db.Task, forceRerun bool) error {
	ready, blockers, err := e.q.CanStartTask(ctx, task.ID)
	if err != nil {
		return err
	}
	if !ready {
		prevStatus := task.Status
		task.Status = db.TaskStatusDependsOnTask
		if _, err := e.q.UpdateTask(ctx, task); err != nil {
			return err
		}
		e.broadcastTaskStatus(task, prevStatus, task.Status, blockers)
		if forceRerun {
			return &TaskDependencyBlockedError{TaskID: task.ID, Blockers: blockers}
		}
		return nil
	}

	prevStatus := task.Status
	task.Status = db.TaskStatusInProgress
	if _, err := e.q.UpdateTask(ctx, task); err != nil {
		return err
	}
	e.broadcastTaskStatus(task, prevStatus, task.Status, nil)
	mode := sessionModeImplement
	go e.run(context.Background(), task, mode)
	return nil
}

func (e *NativeEngine) broadcastTaskStatus(task db.Task, from, to string, blockers []db.Task) {
	payload := map[string]interface{}{"id": task.ID, "status": to}
	if len(blockers) > 0 {
		payload["blocked_by"] = blockers
	}
	e.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", payload)
	e.emitStatusChange(context.Background(), task.ID, from, to)
}

// ReconcileDependents rechecks every task which depends on a completed or
// otherwise changed prerequisite. ProcessTask performs the final dependency
// check and run de-duplication, so repeated reconciliation is safe.
func (e *NativeEngine) ReconcileDependents(ctx context.Context, prerequisiteTaskID int32) {
	dependents, err := e.q.ListDependentTasks(ctx, prerequisiteTaskID)
	if err != nil {
		fmt.Printf("Warning: failed to list dependents of task %d: %v\n", prerequisiteTaskID, err)
		return
	}
	for _, dependent := range dependents {
		if err := e.ProcessTask(ctx, dependent.ID); err != nil {
			fmt.Printf("Warning: failed to reconcile dependent task %d: %v\n", dependent.ID, err)
		}
	}
}

// ReconcileQueuedTasks repairs queued work after a process restart. Tasks
// with an active run are excluded by the query and are handled by run
// recovery/stale-run recovery separately.
func (e *NativeEngine) ReconcileQueuedTasks(ctx context.Context) {
	tasks, err := e.q.ListQueuedTasksForReconciliation(ctx)
	if err != nil {
		fmt.Printf("Warning: failed to list queued tasks for reconciliation: %v\n", err)
		return
	}
	for _, task := range tasks {
		if err := e.ProcessTask(ctx, task.ID); err != nil {
			fmt.Printf("Warning: failed to reconcile queued task %d: %v\n", task.ID, err)
		}
	}
}

// StopRun cancels the context for the given run, interrupting it at the next
// context check inside the agent loop.
func (e *NativeEngine) StopRun(ctx context.Context, runID int32) {
	if val, loaded := e.runs.cancelFuncs.LoadAndDelete(runID); loaded {
		if cancel, ok := val.(context.CancelFunc); ok {
			cancel()
		}
	}
}

// resolveStaleRun marks a stale run as failed and unlocks its task.
func (e *NativeEngine) resolveStaleRun(ctx context.Context, runID int32) {
	run, err := e.q.GetRun(ctx, runID)
	if err != nil {
		return
	}
	e.q.UpdateRunLog(ctx, runID, "Run marked as failed: previous run no longer active", "failed")
	e.broadcastForTask(ctx, run.TaskID, "run_ended", map[string]interface{}{"run_id": runID, "status": "failed"})
	e.q.UnlockTaskRun(ctx, run.TaskID)
}

// run is the goroutine body for a single root agent execution.
func (e *NativeEngine) run(ctx context.Context, task db.Task, mode string) {
	// When the task-orchestrator model is configured, the sidecar is the task
	// owner. It claims the task lock and creates worker children through
	// run_new_session; the assigned agent is only the product-owner identity
	// used to resolve the company's default provider when creating the sidecar.
	if task.AgentID != nil {
		agent, agentErr := e.q.GetAgent(ctx, *task.AgentID)
		if agentErr == nil {
			orchestrator, provider, model, enabled, shouldStart := e.createTaskOrchestrator(ctx, task, agent)
			if enabled {
				claimed, claimErr := e.q.ClaimTaskRun(ctx, task.ID, orchestrator.ID)
				if claimErr != nil {
					_ = e.q.UpdateRunLog(context.Background(), orchestrator.ID, claimErr.Error(), "failed")
					return
				}
				if !claimed {
					// Another active run already owns the task. The existing
					// orchestrator will continue monitoring it.
					return
				}
				if shouldStart {
					e.startTaskOrchestrator(orchestrator, task, provider, model)
				}
				return
			}
			// Orchestration is mandatory. A failed preflight has already recorded
			// a blocked task and visible configuration error; never fall back to
			// the assigned agent's provider/model.
			return
		}
	}
	e.executeSession(ctx, task, mode, nil, nil, sessionOptions{IncludeTaskContext: true})
}

// resumeSession re-enters a previously-paused root run using its persisted
// conversation history instead of building a fresh one from the task. Only
// root runs are ever paused (see BeginDrain/executeSession's pause wiring),
// so this always passes parent = nil.
func (e *NativeEngine) resumeSession(ctx context.Context, task db.Task, run db.Run) {
	e.executeSession(ctx, task, "resume", nil, &run, sessionOptions{
		IncludeTaskContext: true,
		SkipTaskLock:       run.ParentRunID != nil,
	})
}

// ResumeSession claims and asynchronously resumes one checkpointed run. It is
// intentionally code-only: callers choose the recovery policy, while this
// function owns the single reconstruction path for paused, failed, and stale
// sessions. The logical Run.ID is preserved.
func (e *NativeEngine) ResumeSession(ctx context.Context, runID int32, opts ResumeOptions) error {
	run, err := e.q.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("resume run %d: load run: %w", runID, err)
	}
	if run.Recovery.CheckpointVersion != 0 && run.Recovery.CheckpointVersion != db.CheckpointVersion {
		return fmt.Errorf("resume run %d: unsupported checkpoint version %d", runID, run.Recovery.CheckpointVersion)
	}

	cause := opts.Cause
	if cause == "" {
		switch run.Status {
		case db.RunStatusRecoverableFailed, "failed":
			cause = ResumeAfterFailure
		case db.RunStatusStale:
			cause = ResumeAfterStale
		default:
			cause = ResumeAfterUpdate
		}
	}
	allowed := []string{db.RunStatusPaused}
	switch cause {
	case ResumeAfterFailure:
		allowed = []string{db.RunStatusRecoverableFailed, "failed"}
	case ResumeAfterStale:
		allowed = []string{db.RunStatusStale}
	case ResumeAfterUpdate:
	case ResumeAfterHuman:
		allowed = []string{db.RunStatusPaused}
	default:
		return fmt.Errorf("resume run %d: unsupported cause %q", runID, cause)
	}

	owner := fmt.Sprintf("pid-%d-%d", os.Getpid(), time.Now().UnixNano())
	lease := time.Now().Add(2 * time.Minute)
	sequence := run.Recovery.CheckpointSequence
	if sequence <= 0 {
		_, derived, historyErr := aicli.LoadMessageHistoryWithCursor(run.LogFilePath, 0)
		if historyErr != nil {
			_ = e.q.RecordResumeError(ctx, runID, historyErr.Error(), run.Status)
			return fmt.Errorf("resume run %d: derive JSONL checkpoint: %w", runID, historyErr)
		}
		sequence = derived
	}
	claimed, err := e.q.ClaimRunForResume(ctx, runID, owner, string(cause), run.Status, lease, allowed, sequence)
	if err != nil {
		return fmt.Errorf("resume run %d: claim: %w", runID, err)
	}
	if !claimed {
		return fmt.Errorf("resume run %d: already claimed or not eligible", runID)
	}
	run.Recovery.CheckpointSequence = sequence
	run.Recovery.CheckpointVersion = db.CheckpointVersion

	// Carry the claim owner in the in-memory copy. executeSession transitions
	// resuming -> running only after it has rebuilt the runtime successfully.
	run.Recovery.ResumeLeaseOwner = owner
	run.Recovery.ResumePreviousStatus = run.Status
	initiator := run.Recovery.RecoveryInitiator
	if initiator == "" {
		initiator = "system"
	}
	if opts.InitiatorID != nil {
		initiator = fmt.Sprintf("user:%d", *opts.InitiatorID)
	}
	run.Recovery.RecoveryReason = opts.Reason
	if run.Recovery.RecoveryReason == "" {
		run.Recovery.RecoveryReason = string(cause)
	}
	run.Recovery.RecoveryInitiator = initiator
	run.Recovery.RecoveryTarget = opts.TargetBuild
	_ = e.q.UpdateRunRecoveryMetadata(ctx, runID, run.Recovery.RecoveryReason, initiator, opts.TargetBuild)
	task, err := e.q.GetTask(ctx, run.TaskID)
	if err != nil {
		_ = e.q.RecordResumeError(ctx, runID, err.Error(), run.Status)
		return fmt.Errorf("resume run %d: load task: %w", runID, err)
	}
	go e.resumeSession(context.Background(), task, run)
	return nil
}

// ResumeEligibleSessions is the automatic startup policy. Only sessions
// intentionally paused for an update are selected; explicit callers can use
// ResumeSession with failed/stale causes later.
func (e *NativeEngine) ResumeEligibleSessions(ctx context.Context) {
	if err := e.q.ReclaimExpiredResumeLeases(ctx, time.Now()); err != nil {
		fmt.Printf("Warning: failed to reclaim resume leases: %v\n", err)
	}
	runs, err := e.q.GetRunsByRecoveryStates(ctx, []string{db.RunStatusPaused})
	if err != nil {
		fmt.Printf("Warning: failed to list paused runs: %v\n", err)
		return
	}
	if len(runs) == 0 {
		return
	}
	fmt.Printf("Resuming %d paused run(s) after restart...\n", len(runs))
	for _, run := range runs {
		if run.Recovery.RecoveryReason == string(ResumeAfterHuman) {
			// Human-gated sessions remain paused across a restart until the
			// outstanding question receives an answer.
			continue
		}
		if resumeErr := e.ResumeSession(ctx, run.ID, ResumeOptions{Cause: ResumeAfterUpdate}); resumeErr != nil {
			fmt.Printf("Warning: failed to resume run %d: %v\n", run.ID, resumeErr)
		}
	}
}

// executeSession runs one agent session for a task and returns its final run
// status ("completed", "failed", "canceled" or "paused"). Root sessions
// (parent == nil) run detached from the caller's context; delegated child
// sessions inherit the parent's run context so stopping the parent stops the
// whole tree.
//
// resumeRun, when non-nil, re-enters a previously paused root run instead of
// starting a fresh one: its persisted JSONL conversation, selected by the
// Run checkpoint cursor, seeds the agent loop in place of a freshly
// built initial message list, and the existing Run row is reused. Delegated
// sessions still require durable parent coordination before they can pause.
func (e *NativeEngine) executeSession(ctx context.Context, task db.Task, mode string, parent *parentSession, resumeRun *db.Run, options sessionOptions) string {
	if task.AgentID == nil {
		return "failed"
	}
	requestedAgentID := task.AgentID
	if resumeRun != nil {
		resumeAgentID := resumeRun.AgentID
		requestedAgentID = &resumeAgentID
	}

	// Delegated child tasks arrive fresh from CreateTask without preloaded
	// associations (Company, Project, Sprint) — reload so the system prompt
	// and artifact paths see the full task.
	if full, err := e.q.GetTask(ctx, task.ID); err == nil {
		task = full
		// Orchestrator-created worker and fork sessions intentionally run the
		// same root task under a selected agent. The persisted root task keeps
		// the CEO/product-owner assignment, so preserve the explicit child
		// agent across this association reload.
		if requestedAgentID != nil && (parent != nil || options.PrecreatedRun != nil || resumeRun != nil) {
			task.AgentID = requestedAgentID
		}
	}

	agent, err := e.q.GetAgent(ctx, *task.AgentID)
	if err != nil {
		return "failed"
	}

	var run db.Run
	if resumeRun != nil {
		run = *resumeRun
	} else if options.PrecreatedRun != nil {
		run = *options.PrecreatedRun
	} else {
		var orchestrator db.Run
		var orchestratorProvider db.LLMProvider
		var orchestratorModel string
		orchestratorEnabled := false
		orchestratorStart := false
		if parent == nil {
			orchestrator, orchestratorProvider, orchestratorModel, orchestratorEnabled, orchestratorStart = e.createTaskOrchestrator(ctx, task, agent)
		}
		newRun := db.Run{TaskID: task.ID, AgentID: agent.ID, Kind: db.RunKindAgentSession, Status: "running", StartedAt: time.Now()}
		if parent != nil {
			parentID := parent.parentRunID
			rootID := parent.rootRunID
			newRun.ParentRunID = &parentID
			newRun.RootRunID = &rootID
		} else if orchestratorEnabled {
			newRun.ParentRunID = &orchestrator.ID
			newRun.RootRunID = &orchestrator.ID
		}
		created, createErr := e.q.CreateRun(ctx, newRun)
		if createErr != nil {
			return "failed"
		}
		run = created
		if parent == nil {
			if orchestratorEnabled {
				if orchestratorStart {
					e.startTaskOrchestrator(orchestrator, task, orchestratorProvider, orchestratorModel)
				}
			} else {
				// Legacy runs without an orchestrator point at themselves.
				rootID := run.ID
				run.RootRunID = &rootID
				if rootErr := e.q.SetRunRootID(ctx, run.ID, rootID); rootErr != nil {
					fmt.Printf("Warning: failed to set root run id for run %d: %v\n", run.ID, rootErr)
				}
			}
		}
	}

	// Track root sessions and durable workers so a graceful shutdown waits for
	// each resumable run to persist its pause checkpoint. Legacy in-process
	// delegation remains covered by its root session.
	trackForDrain := parent == nil || options.PrecreatedRun != nil || run.Kind == db.RunKindHelperWorker
	if trackForDrain {
		e.runs.activeRoots.Add(1)
		defer e.runs.activeRoots.Done()
	}

	// Register the cancel func and lock the task immediately so that
	// StopRun and test pollers (waitForRunCreated) can observe the run right
	// away — before any potentially slow filesystem or DB work.
	// Child sessions derive from the parent run's context so a parent stop
	// cancels them too.
	baseCtx := context.Background()
	if parent != nil {
		baseCtx = ctx
	}
	runCtx, cancel := context.WithCancel(baseCtx)
	e.runs.cancelFuncs.Store(run.ID, cancel)
	defer func() {
		cancel()
		e.runs.cancelFuncs.Delete(run.ID)
	}()
	if resumeRun == nil && !options.SkipTaskLock {
		claimed, claimErr := e.q.ClaimTaskRun(ctx, task.ID, run.ID)
		if claimErr != nil {
			_ = e.q.UpdateRunLog(context.Background(), run.ID, claimErr.Error(), "failed")
			return "failed"
		}
		if !claimed {
			// Another reconciler won the task race. Do not start this run or
			// clear the other run's task lock.
			_ = e.q.UpdateRunLog(context.Background(), run.ID, "task already claimed by another run", "canceled")
			return "canceled"
		}
	}
	// Heartbeat independently of LLM/tool logging. A provider can legitimately
	// spend minutes inside one request, and a waiting tool may emit no log line;
	// neither should look stale to the recovery monitor.
	e.q.TouchRunLastMessageTime(context.Background(), run.ID)
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				e.q.TouchRunLastMessageTime(context.Background(), run.ID)
			}
		}
	}()

	// LockTaskRun is a conditional UPDATE (WHERE run_id IS NULL): for a fresh
	// run it claims the task; for a resumed run the task is already locked to
	// this same run ID (pausing never unlocks it — see the paused branch
	// below), so this is a harmless no-op that leaves the existing lock as-is.
	if !options.SkipTaskLock {
		if lockErr := e.q.LockTaskRun(ctx, task.ID, run.ID); lockErr != nil {
			fmt.Printf("Warning: failed to lock task %d for run %d: %v\n", task.ID, run.ID, lockErr)
		}
	}
	// paused is set just before returning if this session stops via
	// aicli.ErrPaused. In that case the task must stay locked to this run (no
	// other run may start on it) until the recovery coordinator picks it back up
	// after the restart, so the unlock below is skipped.
	paused := false
	defer func() {
		if paused {
			return
		}
		if options.SkipTaskLock {
			return
		}
		if clearErr := e.q.UnlockTaskRun(context.Background(), task.ID); clearErr != nil {
			fmt.Printf("Warning: failed to unlock task %d: %v\n", task.ID, clearErr)
		}
	}()
	// Every path after Run creation must leave a durable non-running state. The
	// explicit branches below cover expected outcomes; this guard catches a
	// newly-added early return or an unexpected setup error before it can leave
	// a row marked running indefinitely.
	defer func() {
		if paused {
			return
		}
		current, err := e.q.GetRun(context.Background(), run.ID)
		if err == nil && (current.Status == "running" || current.Status == db.RunStatusResuming) {
			e.failRun(context.Background(), run.ID, "session exited without a terminal status")
		}
	}()

	// A resumed run re-enters "running", so it reuses the same run_started
	// event a fresh run emits: the Run Logs UI re-fetches the list on it (the
	// run reappears as active) and no consumer has to learn a new event type.
	e.hub.BroadcastEventForCompany(task.CompanyID, "run_started", run)

	var environment sessionEnvironment
	var preparedRun db.Run
	var environmentErr error
	if options.Worker {
		environment, preparedRun, environmentErr = e.prepareWorkerEnvironment(ctx, &task, run, options)
	} else {
		environment, preparedRun, environmentErr = e.prepareSessionEnvironment(ctx, &task, agent, run, parent, resumeRun != nil)
	}
	if environmentErr != nil {
		e.failRun(ctx, run.ID, environmentErr.Error())
		return "failed"
	}
	run = preparedRun
	defer environment.close()
	company := environment.company
	rootTask := environment.rootTask
	rootRunID := environment.rootRunID
	rootTaskID := environment.rootTaskID
	groupMode := environment.groupMode
	provider := environment.provider
	model := environment.model
	workspacePath := environment.workspacePath
	readOnlyDirs := environment.readOnlyDirs
	artifactDir := environment.artifactDir
	proxyLogger := environment.logger
	gitProject := environment.gitProject
	gitMgr := environment.gitManager

	systemPrompt, initialMessages := e.buildSessionPrompt(
		ctx, agent, task, rootTask, mode, options, workspacePath, readOnlyDirs, artifactDir, rootTaskID, run.ID,
	)
	if options.Worker {
		systemPrompt += "\n\n" + strings.TrimSpace(agentconfig.MustPrompt("utils/worker_init.md"))
	}

	var toolState *sessionToolState
	if options.Worker {
		toolState = e.buildWorkerSessionTools(ctx, task, run, agent, provider, model, workspacePath, readOnlyDirs, proxyLogger)
	} else {
		toolState = e.buildSessionTools(ctx, task, run, agent, company, parent, provider, model, workspacePath, readOnlyDirs, artifactDir, rootRunID, rootTaskID, proxyLogger, mode)
		toolState.consultation = options.Consultation
	}
	registry := toolState.registry
	gatewayAuth := &toolState.gatewayAuth

	allCompanyMCP := options.Worker
	integrations := e.configureSessionIntegrations(ctx, task, agent, registry, systemPrompt, proxyLogger, allCompanyMCP)
	registry = integrations.registry
	systemPrompt = integrations.systemPrompt
	listingCostTotal := integrations.listingCostTotal
	listingCostByServer := integrations.listingCostByServer
	defer integrations.close()
	// Determine agent mode and reasoning level from the database Agent row.
	agentMode := aicli.ModeMessageHistory
	reasoningLevel := agent.ReasoningLevel
	switch agent.ChatType {
	case "compact_thinking":
		agentMode = aicli.ModeCompactThinking
	}

	// Wire the proxy logger as the agent's RunLogger so request/response entries
	// appear in the log file and the DB (identical format to the gateway).
	// The display name comes from the database Agent row.
	agentDisplayName := agent.Name
	// Decrypt the provider key at the point of use. A locked owner surfaces as a
	// clear provider-auth failure downstream rather than a silent empty key.
	apiKey, keyErr := secrets.Default().Decrypt(provider.ApiKeyEncrypted)
	if keyErr != nil {
		e.logInfo(proxyLogger, fmt.Sprintf("Warning: could not decrypt provider key: %v", keyErr))
	}
	llmClient := aicli.NewClient(provider.BaseUrl, apiKey, model)
	if groupMode {
		// The group router picks the real provider+model per request. X-Run-ID
		// lets it write model_switch entries into this run's log; switches-only
		// keeps it from double-logging requests/responses (the agent loop
		// below already does that). The gateway token authenticates this run
		// to the (otherwise locked) local proxy — it is only ever sent to the
		// in-process gateway, never to an external provider.
		gatewayAuth.token = runtokens.Default().Issue(run.ID)
		defer runtokens.Default().Revoke(run.ID)
		if err := gatewayAuth.configure(llmClient, provider); err != nil {
			e.failRun(ctx, run.ID, err.Error())
			return "failed"
		}
	}
	agentCfgObj := aicli.Config{
		Client:                      llmClient,
		Registry:                    registry,
		Mode:                        agentMode,
		ProviderName:                provider.Name,
		AgentName:                   agentDisplayName,
		TerminalTools:               []string{string(aicli.ToolFinishTask)},
		ReasoningLevel:              reasoningLevel,
		MCPListingCostPerTurn:       listingCostTotal,
		MCPServerListingCosts:       listingCostByServer,
		Queries:                     e.q,
		RunID:                       run.ID,
		Logger:                      proxyLogger,
		InitialConversationSequence: run.Recovery.CheckpointSequence,
		BeforeTurn: func(controlCtx context.Context, history []aicli.Message) ([]aicli.Message, error) {
			var messages []aicli.Message
			incoming, incomingErr := e.q.ListUnconsumedEventsForTarget(controlCtx, run.ID, db.RunEventTypeSessionMessage, db.RunEventTypeWorkerFinished)
			if incomingErr != nil {
				return nil, incomingErr
			}
			hasMessage := false
			for _, event := range incoming {
				if event.EventType == db.RunEventTypeSessionMessage {
					hasMessage = true
					break
				}
			}
			if hasMessage {
				registry.Register(tools.NewAnswerMessage(func(answerCtx context.Context, messageID int64, answer string) (string, error) {
					return e.answerRoutedMessage(answerCtx, run, messageID, answer)
				}))
				var b strings.Builder
				b.WriteString(strings.TrimSpace(agentconfig.MustPrompt("utils/incoming_messages.md")) + "\n")
				for _, event := range incoming {
					fmt.Fprintf(&b, "- message_id=%d type=%s: %s\n", event.ID, event.EventType, event.Payload)
				}
				messages = append(messages, aicli.Message{Role: "user", Content: b.String()})
			} else {
				registry.Unregister(string(aicli.ToolAnswerMessage))
			}
			refreshEvents, eventErr := e.q.ListPendingRunEventsForRun(controlCtx, run.ID, db.RunEventTypeStatusRefresh)
			if eventErr != nil {
				return nil, eventErr
			}
			events := refreshEvents
			if len(events) == 0 {
				return messages, nil
			}
			sort.SliceStable(events, func(i, j int) bool {
				if events[i].CreatedAt.Equal(events[j].CreatedAt) {
					return events[i].ID < events[j].ID
				}
				return events[i].CreatedAt.Before(events[j].CreatedAt)
			})
			ids := make([]int64, 0, len(events))
			for _, event := range events {
				ids = append(ids, event.ID)
				if event.EventType == db.RunEventTypeStatusRefresh {
					messages = append(messages, aicli.Message{Role: "user", Content: strings.TrimSpace(agentconfig.MustPrompt("utils/status_refresh.md"))})
					continue
				}
				return nil, fmt.Errorf("unsupported worker control event %d", event.ID)
			}
			if consumeErr := e.q.ConsumeRunEvents(controlCtx, ids); consumeErr != nil {
				return nil, consumeErr
			}
			return messages, nil
		},
		Interrupt:            func(context.Context, []aicli.Message) ([]aicli.Message, error) { return nil, nil },
		HistoryAlreadyLogged: resumeRun != nil,
	}
	if options.Worker {
		agentCfgObj.TerminalTools = []string{string(aicli.ToolFinishWork)}
	}
	if resumeRun != nil {
		initiator := run.Recovery.RecoveryInitiator
		if initiator == "" {
			initiator = "system"
		}
		agentCfgObj.ResumeNotice = fmt.Sprintf("This session was resumed by %s because %s. Continue the existing task from the restored conversation; do not repeat completed work.", initiator, recoveryReason(run))
	}
	aiAgent := aicli.New(agentCfgObj)
	if resumeRun != nil {
		if startErr := e.q.MarkRunResumeStarted(ctx, run.ID, run.Recovery.ResumeLeaseOwner); startErr != nil {
			paused = true
			_ = e.q.RecordResumeError(context.Background(), run.ID, startErr.Error(), run.Recovery.ResumePreviousStatus)
			return "paused"
		}
	}

	taskName := task.RefKey
	if taskName == "" {
		taskName = fmt.Sprintf("%s-%d", strings.ToUpper(company.ShortName), task.ID)
	}
	e.logInfo(proxyLogger, fmt.Sprintf("Starting native agent for task %s (mode=%s model=%s provider=%s)", taskName, mode, model, provider.Name))
	e.logInfo(proxyLogger, fmt.Sprintf("Workspace: %s", workspacePath))
	e.logInfo(proxyLogger, fmt.Sprintf("Agent settings: %s (role=%s chat_type=%s reasoning=%s)", agent.Name, agent.RoleKey, agent.ChatType, agent.ReasoningLevel))

	// Seed the loop's history: a resumed run continues from its persisted
	// conversation (captured mid-turn by a prior pause — see below); a fresh
	// run starts from the system prompt + task-derived initial messages.
	seedHistory := aicli.BuildHistory(systemPrompt, initialMessages)
	if options.SeedHistory != nil {
		// Forks already carry the source conversation's system message. Do not
		// prepend a second system prompt; the copied workspace is already at the
		// same filesystem state as this conversation.
		seedHistory = append([]aicli.Message(nil), options.SeedHistory...)
	}
	if resumeRun != nil {
		loaded, uErr := aicli.LoadMessageHistory(run.LogFilePath, run.Recovery.CheckpointSequence)
		if uErr != nil {
			fmt.Printf("Warning: failed to parse JSONL history for resumed run %d (path=%s seq=%d): %v\n", run.ID, run.LogFilePath, run.Recovery.CheckpointSequence, uErr)
			_ = e.q.RecordResumeError(context.Background(), run.ID, fmt.Sprintf("failed to parse saved conversation: %v", uErr), run.Recovery.ResumePreviousStatus)
			return "paused"
		}
		seedHistory = loaded
		e.logInfo(proxyLogger, fmt.Sprintf("Resuming session %d (%d saved messages)", run.ID, len(seedHistory)))
	}
	if options.SeedHistory != nil {
		// SeedHistory is the source conversation for a fork, so the freshly
		// built system prompt is intentionally not prepended a second time.
		// Rebase its runtime-only workdir/session metadata instead.
		seedHistory = rebaseForkHistoryRuntimeMetadata(seedHistory, run.ID, workspacePath)
	}

	// Root sessions and durable orchestrator-owned child runs pause at safe turn boundaries.
	var pauseFn aicli.PauseRequested
	if parent == nil || options.PrecreatedRun != nil || run.Kind == db.RunKindAgentSession || run.Kind == db.RunKindHelperWorker {
		pauseFn = func() bool {
			return e.runs.draining.Load() || e.humanInputPending(context.Background(), task.ID)
		}
	}

	_, resultHistory, agentErr := aiAgent.RunWithHistory(runCtx, seedHistory, pauseFn)

	if agentErr != nil && errors.Is(agentErr, aicli.ErrPaused) {
		if proxyLogger == nil {
			e.failRun(ctx, run.ID, "update pause failed: canonical JSONL logger is unavailable")
			return "failed"
		}
		if syncErr := proxyLogger.Sync(); syncErr != nil {
			e.logError(proxyLogger, fmt.Sprintf("failed to sync paused run history: %v — failing the run instead", syncErr))
			e.failRun(ctx, run.ID, fmt.Sprintf("update pause failed: could not sync conversation log: %v", syncErr))
			return "failed"
		}
		sequence := aiAgent.ConversationSequence()
		if sequence <= 0 && resumeRun != nil {
			sequence = run.Recovery.CheckpointSequence
		}
		if sequence <= 0 {
			e.failRun(ctx, run.ID, "update pause failed: no canonical conversation message was logged")
			return "failed"
		}
		initiator, target := "", ""
		initiator, target = run.Recovery.RecoveryInitiator, run.Recovery.RecoveryTarget
		pauseReason := string(ResumeAfterUpdate)
		if e.humanInputPending(context.Background(), task.ID) {
			pauseReason = string(ResumeAfterHuman)
		}
		if pErr := e.q.PauseRunWithMetadata(context.Background(), run.ID, sequence, pauseReason, initiator, target, string(db.CheckpointPhaseBeforeTools)); pErr != nil {
			fmt.Printf("Warning: failed to persist paused run %d: %v\n", run.ID, pErr)
		}
		e.logInfo(proxyLogger, fmt.Sprintf("Run paused (%s)", pauseReason))
		e.hub.BroadcastEventForCompany(task.CompanyID, "run_paused", map[string]interface{}{"run_id": run.ID, "status": db.RunStatusPaused})
		paused = true
		return db.RunStatusPaused
	}

	status := runStatusCompleted
	var runErrMsg string

	if agentErr != nil {
		if runCtx.Err() == context.Canceled {
			e.logInfo(proxyLogger, "Run canceled by user")
			if proxyLogger != nil {
				proxyLogger.LogOutcome("canceled", "canceled", toolState.finishResult.Status, agentDisplayName, task.ID, "Run canceled by user")
			}
			e.q.UpdateRunLog(context.Background(), run.ID, "", "canceled")
			e.hub.BroadcastEventForCompany(task.CompanyID, "run_ended", map[string]interface{}{"run_id": run.ID, "status": "canceled"})
			return "canceled"
		}
		status = "failed"
		runErrMsg = agentErr.Error()
		e.logError(proxyLogger, fmt.Sprintf("Agent error: %v", agentErr))
	}

	// If finish_task was not called, force a follow-up turn.
	forcedFinish := false
	finished := sessionFinished(options, toolState)
	if agentErr == nil && !finished {
		forcedFinish = true
		e.logInfo(proxyLogger, "finish_task not called. Sending follow-up to force it.")
		followPrompt := strings.TrimSpace(agentconfig.MustPrompt("utils/forced_finish_task.md"))
		if options.Worker {
			followPrompt = strings.TrimSpace(agentconfig.MustPrompt("utils/forced_finish_work.md"))
		}
		_, followErr := aiAgent.Run(runCtx, systemPrompt, followPrompt)
		if followErr != nil {
			e.logError(proxyLogger, fmt.Sprintf("Follow-up failed: %v", followErr))
			status = "failed"
			runErrMsg = fmt.Sprintf("finish_task was not called and the forced follow-up failed: %v", followErr)
		} else if !sessionFinished(options, toolState) {
			status = "failed"
			if options.Worker {
				runErrMsg = "helper worker ended without calling finish_work, including during the forced follow-up"
			} else {
				runErrMsg = "agent ended without calling finish_task, including during the forced follow-up"
			}
		}
	}

	// Git commit if there are changes.
	if gitProject && gitMgr != nil && status == runStatusCompleted && finishAllowsGit(toolState.finishResult) {
		e.tryGitCommit(ctx, proxyLogger, gitMgr, workspacePath, task, agent, *gatewayAuth)
		// The root task owns the single branch and PR. A child may commit to
		// that branch, but only the root publishes it.
		if parent == nil && task.ProjectID != nil {
			e.publishTaskPR(ctx, proxyLogger, gitMgr, workspacePath, rootTask, toolState.finishResult)
		}
	}

	// Emit final token summary.
	if finalStats, err := e.q.GetRunTokenStats(ctx, run.ID); err == nil {
		e.logInfo(proxyLogger, fmt.Sprintf(
			"=== Token Totals === prompt=%d completion=%d reasoning=%d tool_in=%d tool_out=%d total=%d",
			finalStats.PromptTokens, finalStats.CompletionTokens,
			finalStats.ReasoningTokens, finalStats.ToolInputTokens,
			finalStats.ToolOutputTokens, finalStats.TotalTokens,
		))
	}

	// Close the trajectory with an outcome entry: how the loop terminated
	// and the agent's own verdict. Written last so it's the final line of
	// the run's JSONL log.
	if proxyLogger != nil {
		endReason := "no_finish"
		summary := toolState.finishResult.FinishStatus
		switch {
		case agentErr != nil && errors.Is(agentErr, aicli.ErrMaxTurns):
			endReason = "max_turns"
			summary = agentErr.Error()
		case agentErr != nil:
			endReason = "error"
			summary = agentErr.Error()
		case sessionFinished(options, toolState) && forcedFinish:
			endReason = "finish_task_forced"
		case sessionFinished(options, toolState):
			if options.Worker {
				endReason = string(aicli.ToolFinishWork)
			} else {
				endReason = string(aicli.ToolFinishTask)
			}
		}
		proxyLogger.LogOutcome(status, endReason, toolState.finishResult.Status, agentDisplayName, task.ID, summary)
	}

	if resumeRun != nil && status == "failed" && len(resultHistory) > 0 {
		sequence := aiAgent.ConversationSequence()
		if sequence <= 0 && resumeRun != nil {
			sequence = run.Recovery.CheckpointSequence
		}
		// A failed recovery remains explicitly recoverable; the JSONL trajectory
		// remains the sole source of truth for the conversation.
		if recoverErr := e.q.MarkRunRecoverable(ctx, run.ID, db.RunStatusRecoverableFailed, sequence, runErrMsg); recoverErr == nil {
			status = db.RunStatusRecoverableFailed
		}
	}
	e.q.UpdateRunLog(ctx, run.ID, runErrMsg, status)
	if resumeRun != nil && status != db.RunStatusRecoverableFailed {
		if clearErr := e.q.ClearRunCheckpoint(context.Background(), run.ID); clearErr != nil {
			e.logInfo(proxyLogger, "Warning: failed to clear consumed resume checkpoint: "+clearErr.Error())
		}
	}

	e.broadcastForTask(ctx, run.TaskID, "run_ended", map[string]interface{}{"run_id": run.ID, "status": status})

	// A dependent must not start until this run's Git, result, and cleanup work
	// has finished. Reconcile only an accepted task completion; reopening or
	// review states are handled by the next explicit transition/start gate.
	if !options.Worker && toolState.finishResult.Status == db.TaskStatusDone {
		e.ReconcileDependents(context.Background(), task.ID)
	}

	return status
}

func (e *NativeEngine) publishTaskPR(ctx context.Context, logger *logging.ProxyLogger, gitMgr *git.GitManager, workspace string, task db.Task, finish tools.FinishTaskResult) {
	if task.ProjectID == nil {
		return
	}
	project, err := e.q.GetProject(ctx, *task.ProjectID)
	if err != nil || project.GitHubInstallationID == 0 {
		return
	}
	branch := strings.TrimSpace(task.GitHubBranch)
	if branch == "" {
		branch = db.TaskGitBranch(task.RefKey, task.ID)
		task.GitHubBranch = branch
		if _, err := e.q.UpdateTask(ctx, task); err != nil {
			e.logInfo(logger, "Task branch persistence failed: "+err.Error())
			return
		}
	}
	changed, err := gitMgr.HasChangesFromBase(ctx, workspace, task.EffectiveGitBaseBranch())
	if err != nil {
		e.logInfo(logger, "Git diff against base failed: "+err.Error())
		return
	}
	if !changed {
		e.logInfo(logger, "No committed source changes; skipping push and PR")
		return
	}
	if err := gitMgr.PushWorktreeBranch(ctx, workspace, branch); err != nil {
		e.logInfo(logger, "GitHub push failed: "+err.Error())
		return
	}
	token, err := githubapp.TokenForProject(ctx, project)
	if err != nil {
		e.logInfo(logger, "GitHub installation token failed: "+err.Error())
		return
	}
	gh, err := githubapp.FromEnv()
	if err != nil {
		e.logInfo(logger, "GitHub App client failed: "+err.Error())
		return
	}
	repositorySlug, err := githubapp.RepositorySlug(project.RepositoryUrl)
	if err != nil {
		e.logInfo(logger, "GitHub repository URL is invalid: "+err.Error())
		return
	}
	if task.GitHubPRNumber != 0 {
		e.logInfo(logger, fmt.Sprintf("Updated existing PR #%d: %s", task.GitHubPRNumber, task.GitHubPRURL))
		return
	}
	owner := strings.SplitN(repositorySlug, "/", 2)[0]
	existing, found, err := gh.FindOpenPullRequestByHead(ctx, token, repositorySlug, owner+":"+branch)
	if err != nil {
		e.logInfo(logger, "GitHub PR lookup failed: "+err.Error())
		return
	}
	if found {
		task.GitHubBranch, task.GitHubPRNumber, task.GitHubPRURL = branch, existing.Number, existing.HTMLURL
		_, _ = e.q.UpdateTask(ctx, task)
		e.logInfo(logger, fmt.Sprintf("Reused existing PR #%d: %s", existing.Number, existing.HTMLURL))
		return
	}
	title, description := pullRequestContent(task, finish)
	baseBranch := task.EffectiveGitBaseBranch()
	number, prURL, err := gh.CreatePullRequest(ctx, token, repositorySlug, title, branch, baseBranch, description)
	if err != nil {
		e.logInfo(logger, "GitHub PR creation failed: "+err.Error())
		return
	}
	task.GitHubBranch, task.GitHubPRNumber, task.GitHubPRURL = branch, number, prURL
	_, _ = e.q.UpdateTask(ctx, task)
	e.logInfo(logger, fmt.Sprintf("Created draft PR #%d: %s", number, prURL))
}

func pullRequestContent(task db.Task, finish tools.FinishTaskResult) (string, string) {
	title := strings.TrimSpace(finish.PullRequestTitle)
	if title == "" {
		title = task.Title
	}
	description := strings.TrimSpace(finish.PullRequestDescription)
	if description == "" {
		description = strings.TrimSpace(finish.ResultDetails)
	}
	if description == "" {
		description = strings.TrimSpace(finish.FinishStatus)
	}
	if description == "" {
		description = strings.TrimSpace(task.Description)
	}
	return title, description
}

// buildInitialMessages assembles a session's conversation seed: the task
// description (plus attachment names) as the first user message, followed by
// past run results and human/agent comments interleaved chronologically.
// Delegated subtasks carry no raw user input — their description IS the
// refined description written by the task owner.
func (e *NativeEngine) buildInitialMessages(ctx context.Context, task db.Task, mode string) []aicli.Message {
	comments, _ := e.q.ListCommentsByTask(ctx, task.ID)
	attachments, _ := e.q.ListAttachmentsByTask(ctx, task.ID)
	pastRuns, _ := e.q.ListCompletedRunsByTask(ctx, task.ID)

	taskDesc := task.Description
	if taskDesc == "" {
		taskDesc = task.RefinedDescription
	}
	taskContent := fmt.Sprintf("Task: %s\nDescription: %s\nMode: %s", task.Title, taskDesc, mode)
	if len(attachments) > 0 {
		taskContent += "\nAttachments:"
		for _, a := range attachments {
			if strings.HasPrefix(a.MimeType, "image/") {
				taskContent += fmt.Sprintf("\n- %s (image)", a.Filename)
			} else {
				taskContent += fmt.Sprintf("\n- %s", a.Filename)
			}
		}
	}

	type timelineEntry struct {
		t    time.Time
		role string
		text string
	}
	var timeline []timelineEntry

	// Past completed runs as compact JSON agent messages (description only,
	// not the full explanation).
	for _, r := range pastRuns {
		ts := r.StartedAt
		if r.EndedAt != nil {
			ts = *r.EndedAt
		}
		msg := fmt.Sprintf(`{"run_id":%d,"completed_at":"%s","result":%q}`,
			r.ID, ts.Format(time.RFC3339), r.ResultDescription)
		timeline = append(timeline, timelineEntry{t: ts, role: "assistant", text: msg})
	}

	// Human comments and non-task_done agent comments; status changes and
	// artifact notifications get a readable rendering.
	for _, c := range comments {
		if c.AuthorType == "agent" && c.CommentType == "task_done" {
			continue // already represented by the run result JSON above
		}
		role := "user"
		if c.AuthorType == "agent" || c.AuthorType == "system" {
			role = "assistant"
		}
		text := c.Content
		switch c.CommentType {
		case "status_change":
			var meta struct {
				From string `json:"from"`
				To   string `json:"to"`
			}
			if jsonErr := json.Unmarshal([]byte(c.Content), &meta); jsonErr == nil {
				actor := "User"
				if c.AuthorType != "human" {
					actor = "System"
				}
				text = fmt.Sprintf("[%s changed task status: %s → %s]", actor, meta.From, meta.To)
			}
		case "artifact_created":
			var meta struct {
				Filename string `json:"filename"`
			}
			if jsonErr := json.Unmarshal([]byte(c.Content), &meta); jsonErr == nil {
				text = fmt.Sprintf(`[Artifact created: "%s"]`, meta.Filename)
			}
		}
		timeline = append(timeline, timelineEntry{t: c.CreatedAt, role: role, text: text})
	}

	// Sort chronologically.
	for i := 1; i < len(timeline); i++ {
		for j := i; j > 0 && timeline[j].t.Before(timeline[j-1].t); j-- {
			timeline[j], timeline[j-1] = timeline[j-1], timeline[j]
		}
	}

	messages := []aicli.Message{{Role: "user", Content: taskContent}}
	for _, entry := range timeline {
		messages = append(messages, aicli.Message{Role: entry.role, Content: entry.text})
	}
	return messages
}

func (e *NativeEngine) humanInputPending(ctx context.Context, taskID int32) bool {
	_, pending, err := e.q.FindPendingHumanQuestion(ctx, taskID)
	return err == nil && pending
}

// HandleHumanReply is called after a human task comment is persisted. It is
// the durable control-plane transition for ask_human: unblock the task,
// release the asking run, resume sibling sessions paused at safe boundaries,
// and wake the task orchestrator with a correlated event. It is idempotent;
// ordinary human comments and duplicate delivery do nothing.
func (e *NativeEngine) HandleHumanReply(ctx context.Context, taskID int32) error {
	comments, err := e.q.ListCommentsByTask(ctx, taskID)
	if err != nil {
		return err
	}
	var question, answer db.Comment
	for _, comment := range comments {
		if comment.CommentType == "ask_user" && comment.AuthorType == "agent" {
			question = comment
			answer = db.Comment{}
			continue
		}
		if question.ID != 0 && comment.AuthorType == "human" && comment.ID > question.ID {
			answer = comment
		}
	}
	if question.ID == 0 || answer.ID == 0 {
		return nil
	}

	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status == db.TaskStatusBlocked {
		previous := task.Status
		changed, updateErr := e.q.SetTaskStatusIf(ctx, task.ID, db.TaskStatusBlocked, db.TaskStatusInProgress)
		if updateErr != nil {
			return updateErr
		}
		// Reload after the narrow update so subsequent routing uses the current
		// orchestrator/archive pointers, even if another lifecycle update raced
		// with the human reply.
		updated, reloadErr := e.q.GetTask(ctx, task.ID)
		if reloadErr != nil {
			return reloadErr
		}
		task = updated
		if changed {
			e.broadcastTaskStatus(updated, previous, updated.Status, nil)
		}
	}
	if question.RunID != nil {
		if err := e.q.SetRunRunning(ctx, *question.RunID); err != nil {
			return err
		}
	}

	paused, err := e.q.GetRunsByRecoveryStates(ctx, []string{db.RunStatusPaused})
	if err != nil {
		return err
	}
	for _, run := range paused {
		if run.TaskID != taskID || run.Recovery.RecoveryReason != string(ResumeAfterHuman) {
			continue
		}
		if resumeErr := e.ResumeSession(ctx, run.ID, ResumeOptions{Cause: ResumeAfterHuman, Reason: "human input received"}); resumeErr != nil {
			// Another concurrent delivery may have claimed this same run. The
			// conditional resume lease makes that race harmless.
			current, getErr := e.q.GetRun(ctx, run.ID)
			if getErr != nil || current.Status == db.RunStatusPaused {
				return resumeErr
			}
		}
	}

	if task.OrchestratorRunID != nil {
		payload, _ := json.Marshal(map[string]interface{}{
			"task_id": taskID, "question_comment_id": question.ID,
			"answer_comment_id": answer.ID, "answer": answer.Content,
		})
		_, err = e.q.EnqueueRoutedEvent(ctx, taskID, valueOrZero(question.RunID), *task.OrchestratorRunID,
			db.RunEventTypeHumanInputAnswered, string(payload), fmt.Sprintf("human-answer:%d:%d", question.ID, answer.ID))
		if err != nil {
			return err
		}
	}
	e.broadcastForTask(ctx, taskID, "human_input_answered", map[string]interface{}{
		"task_id": taskID, "question_comment_id": question.ID, "answer_comment_id": answer.ID,
	})
	return nil
}

func valueOrZero(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}

// askHuman posts the agent's question as an ask_user comment and blocks until
// a human replies on the task, returning the reply text. The task-level
// pending question is the durable gate used by sibling sessions and the
// orchestrator watchdog.
func (e *NativeEngine) askHuman(ctx context.Context, taskID, runID int32, question string) (string, error) {
	rid := runID
	questionComment, err := e.q.CreateComment(ctx, db.Comment{
		TaskID:      taskID,
		AuthorType:  "agent",
		CommentType: "ask_user",
		Content:     question,
		RunID:       &rid,
	})
	if err != nil {
		return "", fmt.Errorf("ask_human: failed to post question: %w", err)
	}
	e.broadcastForTask(ctx, taskID, "comment_created", questionComment)
	e.broadcastForTask(ctx, taskID, "human_input_requested", map[string]interface{}{
		"task_id":  taskID,
		"run_id":   runID,
		"question": question,
	})
	_ = e.q.SetRunWaitStateForComment(context.Background(), runID, "awaiting_human_input", questionComment.ID)
	if task, taskErr := e.q.GetTask(ctx, taskID); taskErr == nil && task.Status == db.TaskStatusInProgress {
		prevStatus := task.Status
		task.Status = db.TaskStatusBlocked
		if _, updateErr := e.q.UpdateTask(ctx, task); updateErr == nil {
			e.broadcastTaskStatus(task, prevStatus, task.Status, nil)
		}
	}
	if task, taskErr := e.q.GetTask(ctx, taskID); taskErr == nil && task.OrchestratorRunID != nil {
		payload, _ := json.Marshal(map[string]interface{}{
			"task_id": taskID, "run_id": runID, "question_comment_id": questionComment.ID, "question": question,
		})
		if _, eventErr := e.q.EnqueueRoutedEvent(ctx, taskID, runID, *task.OrchestratorRunID,
			db.RunEventTypeHumanInputRequested, string(payload), fmt.Sprintf("human-question:%d", questionComment.ID)); eventErr != nil {
			return "", fmt.Errorf("ask_human: failed to notify orchestrator: %w", eventErr)
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
		// Keep the run alive so the staleness detector doesn't kill it while
		// we wait for the user.
		e.q.TouchRunLastMessageTime(context.Background(), runID)

		comments, listErr := e.q.ListCommentsByTask(context.Background(), taskID)
		if listErr != nil {
			continue
		}
		for _, c := range comments {
			if c.AuthorType == "human" && c.ID > questionComment.ID {
				// The HTTP/API path normally performs this transition before the
				// polling loop observes the answer. Keep the direct engine path
				// compatible for tests and non-HTTP callers as well.
				_ = e.HandleHumanReply(context.Background(), taskID)
				return c.Content, nil
			}
		}
	}
}

// createBoardTask is the callback behind the create_task tool: it creates a
// new top-level task on the board (mirroring the API's CreateTask endpoint)
// and, when created in "to-do", kicks off its execution as an independent
// root run — exactly as if a human had moved the card there.
func (e *NativeEngine) createBoardTask(ctx context.Context, creator db.Task, agentID int32, company db.Company, p tools.CreateTaskParams) (string, error) {
	status := p.Status
	if status == "" {
		status = "backlog"
	}
	if status != "backlog" && status != "to-do" {
		return "", fmt.Errorf("status must be \"backlog\" or \"to-do\", got %q", status)
	}
	priority := p.Priority
	if priority == "" {
		priority = "Normal"
	}
	selectedAgentID := agentID
	if p.AgentName != "" {
		targetAgent, targetErr := e.findAgentForRole(ctx, company.ID, p.AgentName)
		if targetErr != nil {
			return "", targetErr
		}
		selectedAgentID = targetAgent.ID
	}

	sprintID := creator.SprintID
	if p.SprintID != 0 {
		sprintID = p.SprintID
	}
	projectID := creator.ProjectID
	if p.ProjectID != 0 {
		project, err := e.q.GetProject(ctx, p.ProjectID)
		if err != nil {
			return "", fmt.Errorf("project %d not found", p.ProjectID)
		}
		if project.CompanyID != company.ID {
			return "", fmt.Errorf("project %d belongs to another company", p.ProjectID)
		}
		pid := project.ID
		projectID = &pid
	}
	var dueDate *time.Time
	if p.DueDate != "" {
		t, err := time.Parse(time.RFC3339, p.DueDate)
		if err != nil {
			return "", fmt.Errorf("invalid due_date %q — use RFC3339, e.g. 2026-08-01T00:00:00Z", p.DueDate)
		}
		dueDate = &t
	}

	newTask, err := e.q.CreateTask(ctx, db.Task{
		CompanyID:    company.ID,
		ProjectID:    projectID,
		SprintID:     sprintID,
		AgentID:      &selectedAgentID,
		Title:        p.Title,
		Description:  p.Description,
		Status:       status,
		Priority:     priority,
		DueDate:      dueDate,
		GitHubBranch: creator.GitHubBranch,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create task: %w", err)
	}
	rollback := func(cause error) (string, error) {
		_ = e.q.DeleteTaskRelationsForTask(ctx, newTask.ID)
		_ = e.q.DeleteTask(ctx, newTask.ID)
		return "", cause
	}
	for _, prerequisiteID := range p.DependsOnTaskIDs {
		if _, relationErr := e.q.CreateTaskRelation(ctx, db.TaskRelation{CompanyID: company.ID, SourceTaskID: newTask.ID, TargetTaskID: prerequisiteID, Kind: db.TaskRelationDependsOn}); relationErr != nil {
			return rollback(fmt.Errorf("failed to add dependency on task %d: %w", prerequisiteID, relationErr))
		}
	}
	for _, relatedID := range p.RelatedToTaskIDs {
		if _, relationErr := e.q.CreateTaskRelation(ctx, db.TaskRelation{CompanyID: company.ID, SourceTaskID: newTask.ID, TargetTaskID: relatedID, Kind: db.TaskRelationRelatedTo}); relationErr != nil {
			return rollback(fmt.Errorf("failed to relate task %d: %w", relatedID, relationErr))
		}
	}
	e.hub.BroadcastEventForCompany(newTask.CompanyID, "task_created", newTask)

	if status == "to-do" {
		// Independent root run, same as a human moving the card to "to-do".
		newTaskID := newTask.ID
		go func() {
			if perr := e.ProcessTask(context.Background(), newTaskID); perr != nil {
				fmt.Printf("Warning: failed to start created task %d: %v\n", newTaskID, perr)
			}
		}()
	}

	ref := newTask.RefKey
	if ref == "" {
		ref = fmt.Sprintf("#%d", newTask.ID)
	}
	reply := fmt.Sprintf("Task %s created on the board: %q (status %s, priority %s).", ref, newTask.Title, status, priority)
	if status == "to-do" {
		reply += " It starts executing independently of this session."
	}
	return reply, nil
}

// ownerUserIDForCompany resolves a company's owning user, or 0 when unset —
// Default Models settings are per-user, so internal one-shot LLM calls made
// on behalf of a task use the task owner's configuration.
func (e *NativeEngine) ownerUserIDForCompany(ctx context.Context, companyID int32) int32 {
	company, err := e.q.GetCompany(ctx, companyID)
	if err != nil || company.UserID == nil {
		return 0
	}
	return *company.UserID
}

func (e *NativeEngine) ownerUserIDForCompanyOfTask(ctx context.Context, taskID int32) int32 {
	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return 0
	}
	return e.ownerUserIDForCompany(ctx, task.CompanyID)
}

// resolvePurposeModel resolves the provider/model pair configured for one
// internal-use purpose (see db.PurposeCommitMessages, db.PurposeAskArtifact)
// via the "Default Models" settings — independent of any Model Group's own
// definition. A purpose can point at a fixed provider+model or at any model
// group (in which case its free members are tried first, tie-broken by
// priority — the same ordering the group gateway uses, minus live health
// tracking, which isn't worth the bookkeeping for an infrequent one-shot
// call). Falls back to the session's own provider and model when the
// purpose has no override configured, or its target no longer resolves.
func (e *NativeEngine) resolvePurposeModel(ctx context.Context, userID int32, purpose string, sessionProvider db.LLMProvider, sessionModel string) (db.LLMProvider, string) {
	setting, err := e.q.GetDefaultModelSetting(ctx, userID, purpose)
	if err != nil {
		return sessionProvider, sessionModel
	}

	if setting.ModelGroupID != nil {
		group, gErr := e.q.GetModelGroup(ctx, *setting.ModelGroupID)
		if gErr != nil {
			return sessionProvider, sessionModel
		}
		provider, model, targetErr := resolveModelGroupTarget(group)
		if targetErr != nil {
			return sessionProvider, sessionModel
		}
		return provider, model
	}

	if setting.ProviderID != nil {
		provider, pErr := e.q.GetLLMProvider(ctx, *setting.ProviderID)
		if pErr != nil {
			return sessionProvider, sessionModel
		}
		model := setting.Model
		if model == "" {
			model = provider.DefaultModel
		}
		return provider, model
	}

	return sessionProvider, sessionModel
}

// resolveRequiredPurposeModel resolves only the configured internal purpose.
// It deliberately has no session-provider fallback: control-plane work must
// not silently run on the assigned agent's model.
func (e *NativeEngine) resolveRequiredPurposeModel(ctx context.Context, userID int32, purpose string) (db.LLMProvider, string, error) {
	setting, err := e.q.GetDefaultModelSetting(ctx, userID, purpose)
	if err != nil {
		return db.LLMProvider{}, "", fmt.Errorf("required model setting %q is unavailable: %w", purpose, err)
	}
	if setting.ModelGroupID != nil {
		group, err := e.q.GetModelGroup(ctx, *setting.ModelGroupID)
		if err != nil {
			return db.LLMProvider{}, "", fmt.Errorf("model group for %q cannot be resolved: %w", purpose, err)
		}
		provider, model, targetErr := resolveModelGroupTarget(group)
		if targetErr != nil {
			return db.LLMProvider{}, "", fmt.Errorf("model group for %q has no usable members: %w", purpose, targetErr)
		}
		return provider, model, nil
	}
	if setting.ProviderID == nil {
		return db.LLMProvider{}, "", fmt.Errorf("required model setting %q is not configured", purpose)
	}
	provider, err := e.q.GetLLMProvider(ctx, *setting.ProviderID)
	if err != nil {
		return db.LLMProvider{}, "", fmt.Errorf("provider for %q cannot be resolved: %w", purpose, err)
	}
	model := strings.TrimSpace(setting.Model)
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	if model == "" {
		return db.LLMProvider{}, "", fmt.Errorf("required model setting %q has no model", purpose)
	}
	return provider, model, nil
}

func (e *NativeEngine) resolveHelperWorkerModel(ctx context.Context, userID int32) (db.LLMProvider, string, error) {
	setting, settingErr := e.q.GetDefaultModelSetting(ctx, userID, db.PurposeHelperWorker)
	if settingErr == nil && (setting.ProviderID != nil || setting.ModelGroupID != nil) {
		// An explicit helper override is independent. If it is invalid, fail
		// rather than silently switching models.
		return e.resolveRequiredPurposeModel(ctx, userID, db.PurposeHelperWorker)
	}
	// An empty helper setting means "use orchestrator model". Resolve that
	// setting directly; never pass the parent session provider as a fallback.
	return e.resolveRequiredPurposeModel(ctx, userID, db.PurposeTaskOrchestrator)
}

// formatArtifactList renders artifact metadata (never content) for agent
// system prompts: name, size, line count, modify time, verified flag and the
// producer's one-line description.
func formatArtifactList(arts []db.Artifact) string {
	var b strings.Builder
	for _, a := range arts {
		lines := strings.Count(a.Content, "\n") + 1
		verified := "no"
		if a.IsVerified {
			verified = "yes"
		}
		fmt.Fprintf(&b, "- %s (%d bytes, %d lines, modified %s, verified: %s)",
			a.Filename, len(a.Content), lines, a.UpdatedAt.Format("2006-01-02 15:04"), verified)
		if a.Description != "" {
			fmt.Fprintf(&b, " — %s", a.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// broadcastForTask delivers a tenant-scoped event by resolving the task's
// company owner; when the task can't be loaded the event goes to in-process
// subscribers only (fail closed for WS clients).
func (e *NativeEngine) broadcastForTask(ctx context.Context, taskID int32, event string, payload interface{}) {
	if task, err := e.q.GetTask(ctx, taskID); err == nil {
		e.hub.BroadcastEventForCompany(task.CompanyID, event, payload)
		return
	}
	e.hub.BroadcastEventForCompany(-1, event, payload)
}

// emitStatusChange creates a status_change comment and broadcasts it.
func (e *NativeEngine) emitStatusChange(ctx context.Context, taskID int32, from, to string) {
	content, _ := json.Marshal(map[string]string{"from": from, "to": to})
	comment, err := e.q.CreateComment(ctx, db.Comment{
		TaskID:      taskID,
		AuthorType:  "system",
		CommentType: "status_change",
		Content:     string(content),
	})
	if err == nil {
		e.broadcastForTask(ctx, taskID, "comment_created", comment)
	}
}

// failRun marks a run as failed and broadcasts the event.
func (e *NativeEngine) failRun(ctx context.Context, runID int32, errMsg string) {
	e.q.UpdateRunLog(ctx, runID, errMsg, "failed")
	payload := map[string]interface{}{"run_id": runID, "status": "failed"}
	if run, err := e.q.GetRun(ctx, runID); err == nil {
		e.broadcastForTask(ctx, run.TaskID, "run_ended", payload)
	} else {
		e.hub.BroadcastEventForCompany(-1, "run_ended", payload) // owner unknown — subscribers only
	}
}

// logInfo writes an info entry to the proxy logger (if non-nil).
func (e *NativeEngine) logInfo(logger *logging.ProxyLogger, msg string) {
	if logger == nil {
		fmt.Println(msg)
		return
	}
	logger.LogInfo(msg)
}

// logError writes an error entry to the proxy logger (if non-nil).
func (e *NativeEngine) logError(logger *logging.ProxyLogger, msg string) {
	if logger == nil {
		fmt.Println("ERROR:", msg)
		return
	}
	logger.LogErrorMsg(msg)
}

// tryGitCommit generates a commit message and commits workspace changes.
func (e *NativeEngine) tryGitCommit(ctx context.Context, logger *logging.ProxyLogger, gitMgr *git.GitManager, workspacePath string, task db.Task, agent db.Agent, gatewayAuth runGatewayAuth) bool {
	// Skip cleanly when the workspace is not a git worktree (e.g. worktree
	// creation failed or the task has a bare directory workspace) instead of
	// letting `git diff` fail with usage noise.
	if _, statErr := os.Stat(filepath.Join(workspacePath, ".git")); statErr != nil {
		e.logInfo(logger, "Workspace is not a git worktree; skipping commit")
		return false
	}
	e.logInfo(logger, "Checking for changes to commit in worktree...")
	status, err := gitMgr.GetStatusInDir(ctx, workspacePath)
	if err != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to get worktree status: %v", err))
		return false
	}
	status = commitRelevantGitStatus(status)
	if strings.TrimSpace(status) == "" {
		e.logInfo(logger, "No changes to commit")
		return false
	}
	diff, err := gitMgr.GetDiffInDir(ctx, workspacePath)
	if err != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to get diff: %v", err))
		return false
	}
	// git diff omits untracked and staged-only changes. Status proves that a
	// commit is needed and still gives the message generator useful context.
	if strings.TrimSpace(diff) == "" {
		diff = status
	}
	// Generate commit message via LLM.
	commitMsg, msgErr := e.generateCommitMessage(ctx, agent, diff, task, gatewayAuth)
	if msgErr != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to generate commit message: %v, using fallback", msgErr))
		commitMsg = fmt.Sprintf("Agent run for task %d", task.ID)
	}

	if commitErr := gitMgr.CommitInWorktree(ctx, workspacePath, commitMsg); commitErr != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to commit: %v", commitErr))
		return false
	} else {
		e.logInfo(logger, fmt.Sprintf("Committed changes: %s", commitMsg))
		return true
	}
}

// generateCommitMessage calls the LLM to summarise a diff into a commit message.
func (e *NativeEngine) generateCommitMessage(ctx context.Context, agent db.Agent, diff string, task db.Task, gatewayAuth runGatewayAuth) (string, error) {
	if agent.ProviderID == nil {
		return "", fmt.Errorf("no provider configured")
	}
	provider, err := e.q.GetLLMProvider(ctx, *agent.ProviderID)
	if err != nil {
		return "", err
	}
	// Commit messages are a lightweight internal task: prefer the configured
	// "Default Models" target for it, when one is set.
	sessionModel := agent.Model
	if sessionModel == "" {
		sessionModel = provider.DefaultModel
	}
	provider, model := e.resolvePurposeModel(ctx, e.ownerUserIDForCompany(ctx, task.CompanyID), db.PurposeCommitMessages, provider, sessionModel)
	const maxDiffChars = 60000
	if len(diff) > maxDiffChars {
		diff = diff[:maxDiffChars] + "\n... (truncated)"
	}
	prompt := fmt.Sprintf(`Summarize these code changes into a concise git commit message.
Subject line max 72 chars. Optional body separated by blank line.
Respond with ONLY the commit message, no quotes or explanation.

Task: %s
Changes:
%s`, task.Title, diff)

	apiKey, err := secrets.Default().Decrypt(provider.ApiKeyEncrypted)
	if err != nil {
		return "", fmt.Errorf("commit message: decrypt provider key: %w", err)
	}
	client := aicli.NewClient(provider.BaseUrl, apiKey, model)
	if err := gatewayAuth.configure(client, provider); err != nil {
		return "", fmt.Errorf("commit message: %w", err)
	}
	resp, _, err := client.Complete(ctx, aicli.ChatRequest{
		Messages:  []aicli.Message{{Role: "user", Content: prompt}},
		MaxTokens: 200,
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	msg := strings.TrimSpace(resp.Choices[0].Message.Content)
	msg = strings.Trim(msg, "\"'`")
	if msg == "" {
		return "", fmt.Errorf("empty commit message")
	}

	return msg, nil
}

// finishAllowsGit limits repository publication to successful agent verdicts.
// A blocked result may have touched the worktree while preparing a handoff,
// but it must not be turned into an automatic commit or PR.
func finishAllowsGit(result tools.FinishTaskResult) bool {
	return result.Status == "done" || result.Status == "in-review"
}

// commitRelevantGitStatus removes the per-task memory file from publication.
// It is created automatically in every task worktree and is execution
// metadata, not a project change.
func commitRelevantGitStatus(status string) string {
	var relevant []string
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) >= 3 && strings.TrimSpace(line[3:]) == "memory.md" {
			continue
		}
		relevant = append(relevant, line)
	}
	return strings.Join(relevant, "\n")
}
