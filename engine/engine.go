package engine

import (
	"context"
	"fmt"

	"agent-orchestrator/db"
)

// TaskDependencyBlockedError is returned when a task is asked to run while
// tasks it depends on are unfinished.
type TaskDependencyBlockedError struct {
	TaskID   int32
	Blockers []db.Task
}

func (e *TaskDependencyBlockedError) Error() string {
	return fmt.Sprintf("task %d depends on unfinished task(s)", e.TaskID)
}

// Engine is the contract used by the server layer.
type Engine interface {
	// ProcessTask asks the workflow to look at a task because something about
	// it changed: its status, a dependency, a relation. It is always safe to
	// call; a task that needs nothing is left as it is.
	ProcessTask(ctx context.Context, taskID int32) error
	// RerunTask sends a finished, parked or stuck top-level task back to work.
	RerunTask(ctx context.Context, taskID int32) error
	// StopTask halts a task and everything under it.
	StopTask(ctx context.Context, taskID int32) error
	// SetTaskStatus puts a task that is at rest into the backlog, into review
	// or to done. It returns ErrTaskRunning for a task that is queued or
	// being worked on.
	SetTaskStatus(ctx context.Context, taskID int32, status string) error
	// StopRun ends one executor session.
	StopRun(ctx context.Context, runID int32)
	// HandleHumanReply tells the workflow that a human commented on a task,
	// which may answer a question some task in its tree is waiting on.
	HandleHumanReply(ctx context.Context, taskID int32) error
	// NotifyCredentialsChanged tells the workflow that a model may have
	// become usable: a vault was unlocked or a default model was set.
	NotifyCredentialsChanged()
}
