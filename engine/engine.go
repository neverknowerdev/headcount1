package engine

import (
	"context"
	"fmt"

	"agent-orchestrator/db"
)

// TaskDependencyBlockedError is returned for explicit reruns that cannot
// start because one or more hard prerequisites are unfinished.
type TaskDependencyBlockedError struct {
	TaskID   int32
	Blockers []db.Task
}

func (e *TaskDependencyBlockedError) Error() string {
	return fmt.Sprintf("task %d depends on unfinished task(s)", e.TaskID)
}

// Engine is the contract used by the server layer.
type Engine interface {
	// ProcessTask reacts to a task's current status and hard dependencies; it
	// starts only queued work whose dependencies are all done.
	ProcessTask(ctx context.Context, taskID int32) error
	// RerunTask forces a new agent run for an explicit user action, moving the
	// task back to in-progress from a terminal status if necessary.
	RerunTask(ctx context.Context, taskID int32) error
	StopRun(ctx context.Context, runID int32)
}
