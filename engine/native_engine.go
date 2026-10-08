package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/appsettings"
	"agent-orchestrator/pkg/logging"

	"gorm.io/gorm"
)

// NativeEngine runs tasks. Its workflow driver moves each task through its
// phases, calling smart models to decide; executor sessions on the cheap tier
// do the work those decisions delegate.
type NativeEngine struct {
	q    *db.Queries
	hub  *eventhub.Hub
	runs *runRegistry
	// driver moves tasks through the workflow; executorSlots counts the
	// executor sessions running, and worktreeLocks serializes the creation of
	// each task tree's git worktree.
	driver        *workflowDriver
	executorSlots atomic.Int32
	worktreeLocks sync.Map
	staleAfter    time.Duration
}

// NewNativeEngine creates the engine. Nothing runs until Start is called.
func NewNativeEngine(database *gorm.DB, hub *eventhub.Hub) *NativeEngine {
	e := &NativeEngine{
		q:    db.New(database),
		hub:  hub,
		runs: newRunRegistry(),

		staleAfter: executorStaleAfter,
	}
	e.driver = newWorkflowDriver(e.q, hub)
	e.driver.startExecutor = e.launchExecutor
	e.driver.reserveExecutorSlot = e.reserveExecutorSlot
	e.driver.releaseExecutorSlot = e.releaseExecutorSlot
	e.driver.reapSessions = e.reapSessions
	e.driver.stopSessions = e.stopSessions
	e.driver.publishPR = e.publishPR
	return e
}

// Options tune how quickly the engine notices that something needs it.
// Zero values use the defaults.
type Options struct {
	// SweepInterval is how often every unfinished task is looked at again
	// whether or not anything asked for it.
	SweepInterval time.Duration
	// StaleAfter is how long an executor session may go without a heartbeat
	// before it is taken for dead.
	StaleAfter time.Duration
	// ModelBackoff is the pause after a model could not be called before it
	// is tried again; it doubles with each further failure in a row.
	ModelBackoff time.Duration
}

// Start begins moving tasks: it resumes the executor sessions a planned
// restart paused, then runs the driver until ctx ends. The driver's first
// sweep picks up every task the previous process left unfinished.
func (e *NativeEngine) Start(ctx context.Context, options Options) {
	if options.ModelBackoff > 0 {
		e.driver.budgets.SmartBackoff = options.ModelBackoff
	}
	if options.SweepInterval > 0 {
		e.driver.sweepInterval = options.SweepInterval
	}
	if options.StaleAfter > 0 {
		e.staleAfter = options.StaleAfter
	}
	e.resumePausedExecutors(ctx)
	e.driver.Start(ctx)
}

func (e *NativeEngine) ProcessTask(_ context.Context, taskID int32) error {
	e.driver.Enqueue(taskID)
	return nil
}

func (e *NativeEngine) RerunTask(ctx context.Context, taskID int32) error {
	return e.driver.Rerun(ctx, taskID)
}

func (e *NativeEngine) StopTask(ctx context.Context, taskID int32) error {
	return e.driver.Stop(ctx, taskID)
}

func (e *NativeEngine) SetTaskStatus(ctx context.Context, taskID int32, status string) error {
	return e.driver.SetStatus(ctx, taskID, status)
}

// StopRun cancels an executor session. The session records itself as
// canceled, and its task is then canceled by its next workflow step.
func (e *NativeEngine) StopRun(_ context.Context, runID int32) {
	if cancel, ok := e.runs.cancelFuncs.Load(runID); ok {
		cancel.(context.CancelFunc)()
	}
}

func (e *NativeEngine) HandleHumanReply(ctx context.Context, taskID int32) error {
	return e.driver.HumanReplied(ctx, taskID)
}

func (e *NativeEngine) NotifyCredentialsChanged() {
	e.driver.CredentialsChanged(context.Background())
}

// stopSessions ends the executor sessions of the given tasks: live ones are
// canceled, and ones a restart had paused are marked canceled directly, since
// no goroutine is there to do it.
func (e *NativeEngine) stopSessions(ctx context.Context, taskIDs []int32) {
	for _, taskID := range taskIDs {
		runs, err := e.q.ListRunsByTask(ctx, taskID)
		if err != nil {
			continue
		}
		for _, run := range runs {
			switch run.Status {
			case "running", db.RunStatusResuming:
				e.StopRun(ctx, run.ID)
			case db.RunStatusPaused:
				_ = e.q.UpdateRunLog(ctx, run.ID, "stopped", "canceled")
			}
		}
	}
}

// BeginDrain prepares for a graceful shutdown: no new executor session
// starts, and each running one pauses at its next turn boundary, leaving a
// cursor from which it is resumed after the restart.
func (e *NativeEngine) BeginDrain() {
	e.runs.beginDrain()
}

// WaitForActiveRuns blocks until every executor session has ended or paused,
// or ctx ends.
func (e *NativeEngine) WaitForActiveRuns(ctx context.Context) {
	e.runs.waitForActiveRuns(ctx)
}

// ownerUserIDForCompany resolves a company's owning user, or 0 when unset.
func (e *NativeEngine) ownerUserIDForCompany(ctx context.Context, companyID int32) int32 {
	company, err := e.q.GetCompany(ctx, companyID)
	if err != nil || company.UserID == nil {
		return 0
	}
	return *company.UserID
}

func (e *NativeEngine) logInfo(logger *logging.ProxyLogger, msg string) {
	if logger == nil {
		fmt.Println(msg)
		return
	}
	logger.LogInfo(msg)
}

func loadSettings() appsettings.Settings {
	return appsettings.Load()
}

// commitRelevantGitStatus removes the per-task memory file from a worktree
// status: it is created in every task worktree and is not a project change.
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
