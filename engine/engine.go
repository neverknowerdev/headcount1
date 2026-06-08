package engine

import "context"

// Engine is the contract used by the server layer. The production engine is
// OpenCodeEngine; the E2E engine makes a direct chat-completions call and
// avoids the opencode server entirely.
type Engine interface {
	ProcessTask(ctx context.Context, taskID int32) error
	StopRun(ctx context.Context, runID int32)
}
