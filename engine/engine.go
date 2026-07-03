package engine

import "context"

// Engine is the contract used by the server layer.
type Engine interface {
	// ProcessTask reacts to a task's current status; it only starts a run for
	// statuses that imply pending work (to-do, in-progress).
	ProcessTask(ctx context.Context, taskID int32) error
	// RerunTask forces a new agent run for an explicit user action, moving the
	// task back to in-progress from a terminal status if necessary.
	RerunTask(ctx context.Context, taskID int32) error
	StopRun(ctx context.Context, runID int32)
}
