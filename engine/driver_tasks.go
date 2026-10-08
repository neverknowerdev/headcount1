package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/workflow"
)

// errTaskBusy means a task's lease could not be taken in time: another driver
// is working on it and did not let go.
var errTaskBusy = errors.New("the task is busy; try again in a moment")

// withLease runs fn on a task while holding its lease, waiting a short while
// for a pass in progress to finish. It is how something other than the task's
// own advance — a user's stop or rerun — changes the task without ever
// writing beside the driver.
func (d *workflowDriver) withLease(ctx context.Context, taskID int32, fn func(l *loadedTask, owner string) error) error {
	owner := fmt.Sprintf("%s/user/%d", d.instance, d.now().UnixNano())
	for attempt := 0; ; attempt++ {
		held, err := d.q.AcquireTaskLease(ctx, taskID, owner, taskLeaseTTL)
		if err != nil {
			return err
		}
		if held {
			break
		}
		if attempt >= 100 {
			return errTaskBusy
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer func() { _ = d.q.ReleaseTaskLease(context.Background(), taskID, owner) }()
	loaded, err := d.load(ctx, taskID)
	if err != nil {
		return err
	}
	return fn(loaded, owner)
}

// Stop halts a task and everything under it. A top-level task parks until the
// user runs it again; a subtask is canceled, which its parent then deals with.
func (d *workflowDriver) Stop(ctx context.Context, taskID int32) error {
	// Interrupt a model call in flight rather than waiting it out.
	if cancel, ok := d.advancing.Load(taskID); ok {
		cancel.(context.CancelFunc)()
	}
	var committed *committedStep
	err := d.withLease(ctx, taskID, func(l *loadedTask, owner string) error {
		switch l.task.Status {
		case models.TaskStatusTodo, models.TaskStatusDependsOnTask, models.TaskStatusInProgress, models.TaskStatusBlocked:
		default:
			return nil // not running: nothing to stop
		}
		var err error
		committed, err = d.apply(ctx, l, workflow.Stop(l.snapshot), applyExtras{byHuman: true}, owner)
		return err
	})
	if err != nil || committed == nil {
		return err
	}
	d.halt(ctx, taskID, committed)
	return nil
}

// halt finishes a committed stop: it ends the sessions and model calls of the
// task and of everything canceled under it, and tells whoever is watching.
func (d *workflowDriver) halt(ctx context.Context, taskID int32, committed *committedStep) {
	for _, id := range committed.canceled {
		if cancel, ok := d.advancing.Load(id); ok {
			cancel.(context.CancelFunc)()
		}
	}
	d.stopSessions(ctx, append([]int32{taskID}, committed.canceled...))
	d.afterCommit(ctx, committed, applyExtras{})
	for _, id := range committed.canceled {
		if task, err := d.q.GetTask(ctx, id); err == nil {
			d.broadcast(task.CompanyID, "task_updated", task)
		}
	}
}

// ErrTaskRunning means a user tried to move a task the workflow is working
// on. It has to be stopped first.
var ErrTaskRunning = errors.New("the task is running; stop it first")

// SetStatus puts a task that is at rest into the backlog, into review or to
// done, as its user asked. A task that is queued or running is refused, so a
// manual move never races the workflow.
func (d *workflowDriver) SetStatus(ctx context.Context, taskID int32, status string) error {
	var committed *committedStep
	err := d.withLease(ctx, taskID, func(l *loadedTask, owner string) error {
		switch l.task.Status {
		case status:
			return nil
		case models.TaskStatusTodo, models.TaskStatusDependsOnTask, models.TaskStatusInProgress:
			return ErrTaskRunning
		}
		var err error
		committed, err = d.apply(ctx, l, workflow.Place(l.snapshot, status), applyExtras{byHuman: true}, owner)
		return err
	})
	if err != nil || committed == nil {
		return err
	}
	d.halt(ctx, taskID, committed)
	return nil
}

// Rerun runs a top-level task again: a task that is finished, in review,
// parked or stuck goes back to work, re-planning from what it has. Asking for
// a subtask reruns the task at the top of its tree. A task that is already
// running is left alone.
func (d *workflowDriver) Rerun(ctx context.Context, taskID int32) error {
	task, err := d.q.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.RootTaskID != 0 && task.RootTaskID != task.ID {
		taskID = task.RootTaskID
	}
	var committed *committedStep
	err = d.withLease(ctx, taskID, func(l *loadedTask, owner string) error {
		switch l.task.Status {
		case models.TaskStatusBacklog:
			// Never started: queue it and let it begin normally.
			var err error
			committed, err = d.apply(ctx, l, workflow.Queue(), applyExtras{byHuman: true}, owner)
			return err
		case models.TaskStatusTodo, models.TaskStatusDependsOnTask, models.TaskStatusInProgress:
			return nil
		case models.TaskStatusBlocked:
			if l.task.WaitingOn == models.TaskWaitHuman {
				// It is waiting for an answer, not for a rerun.
				return nil
			}
		}
		ready, blockers, err := d.q.CanStartTask(ctx, taskID)
		if err != nil {
			return err
		}
		if !ready {
			return &TaskDependencyBlockedError{TaskID: taskID, Blockers: blockers}
		}
		direction, err := d.directionSince(ctx, l)
		if err != nil {
			return err
		}
		hasSpec := strings.TrimSpace(l.task.RefinedDescription) != ""
		committed, err = d.apply(ctx, l, workflow.Rerun(l.snapshot, hasSpec, direction), applyExtras{byHuman: true}, owner)
		return err
	})
	if err != nil {
		return err
	}
	if committed != nil {
		d.afterCommit(ctx, committed, applyExtras{})
	}
	d.Enqueue(taskID)
	return nil
}

// directionSince gathers what the human wrote on a task after its last
// workflow step: the reason they are running it again.
func (d *workflowDriver) directionSince(ctx context.Context, l *loadedTask) (string, error) {
	var since time.Time
	if len(l.steps) > 0 {
		since = l.steps[len(l.steps)-1].CreatedAt
	}
	comments, err := d.q.ListCommentsByTask(ctx, l.task.ID)
	if err != nil {
		return "", err
	}
	var said []string
	for _, comment := range comments {
		if comment.AuthorType == "human" && comment.CommentType == "" && comment.CreatedAt.After(since) {
			said = append(said, strings.TrimSpace(comment.Content))
		}
	}
	if len(said) == 0 {
		return "The human asked for this task to be run again.", nil
	}
	return "The human asked for this task to be run again and said:\n" + strings.Join(said, "\n\n"), nil
}

// HumanReplied looks again at every task in a tree that is waiting for the
// human, now that they have written something on it. Whether the comment
// answers a task's question is decided when that task is evaluated.
func (d *workflowDriver) HumanReplied(ctx context.Context, taskID int32) error {
	task, err := d.q.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	root := task.RootTaskID
	if root == 0 {
		root = task.ID
	}
	waiting, err := d.q.ListTasksWaitingOn(ctx, root, models.TaskWaitHuman)
	if err != nil {
		return err
	}
	for _, id := range waiting {
		d.Enqueue(id)
	}
	return nil
}

// CredentialsChanged looks again at every task waiting for a model to become
// usable: a vault was unlocked, or a default model was set.
func (d *workflowDriver) CredentialsChanged(ctx context.Context) {
	for _, wait := range []string{models.TaskWaitVault, models.TaskWaitConfig} {
		ids, err := d.q.ListAllTasksWaitingOn(ctx, wait)
		if err != nil {
			continue
		}
		for _, id := range ids {
			d.Enqueue(id)
		}
	}
}
