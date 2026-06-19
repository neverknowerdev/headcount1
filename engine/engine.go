package engine

import "context"

// Engine is the contract used by the server layer.
type Engine interface {
	ProcessTask(ctx context.Context, taskID int32) error
	StopRun(ctx context.Context, runID int32)
}
