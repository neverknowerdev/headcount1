// Package workflow is the task workflow's rulebook: which phases a task of
// each type goes through, which tools a smart model may answer with in each
// phase, what each answer does, and how a prompt is put together.
//
// Everything here is pure. It reads a snapshot of a task and returns what
// should happen next; it never touches the database, a model, or the clock.
// The engine's driver loads the snapshot, performs what this package decides,
// and persists the result in one transaction.
package workflow

import "time"

// Budgets bound every loop in the workflow, so a task always terminates:
// when one is exhausted the task escalates instead of trying again.
type Budgets struct {
	// MaxSmartSteps caps the smart-model calls of one task between starts.
	MaxSmartSteps int
	// MaxAdjustCycles caps how often a task may re-plan after failures.
	MaxAdjustCycles int
	// MaxSmartFailures is how many smart calls in a row may fail before the
	// task stops retrying.
	MaxSmartFailures int
	// MaxInspectsPerPhase caps read-only tool calls (decision tree, execution
	// state) before the model must act.
	MaxInspectsPerPhase int
	// MaxManagedDepth is the deepest level at which a subtask may itself be
	// driven by a smart model; below it subtasks are always direct.
	MaxManagedDepth int
	// MaxQuestionsPerStep and MaxTasksPerStep cap how much one smart answer
	// may delegate.
	MaxQuestionsPerStep int
	MaxTasksPerStep     int
	// MaxExecutorAttempts caps executor sessions per direct task, counting the
	// first; MaxExecutorTurns caps the model round trips of one session.
	MaxExecutorAttempts int
	MaxExecutorTurns    int
	// MaxReviewRounds caps the fix-and-review-again rounds the engine runs on
	// its own before handing a failing review to a smart model.
	MaxReviewRounds int
	// CheckpointEveryToolCalls forces an executor checkpoint after this many
	// tool calls without one.
	CheckpointEveryToolCalls int
	// SmartBackoff is the wait after the first failed smart call; it doubles
	// with each further consecutive failure.
	SmartBackoff time.Duration
}

// DefaultBudgets are the limits used in production.
var DefaultBudgets = Budgets{
	MaxSmartSteps:            40,
	MaxAdjustCycles:          3,
	MaxSmartFailures:         3,
	MaxInspectsPerPhase:      4,
	MaxManagedDepth:          2,
	MaxQuestionsPerStep:      8,
	MaxTasksPerStep:          12,
	MaxExecutorAttempts:      2,
	MaxExecutorTurns:         150,
	MaxReviewRounds:          2,
	CheckpointEveryToolCalls: 12,
	SmartBackoff:             30 * time.Second,
}

// backoff is the wait before retrying after the nth consecutive failure
// (n starts at 1).
func (b Budgets) backoff(n int) time.Duration {
	wait := b.SmartBackoff
	for i := 1; i < n; i++ {
		wait *= 2
	}
	return wait
}
