package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

// WorkflowRepository is the only way workflow state is written.
//
// The rule it enforces: a task's workflow columns are changed only by that
// task's own driver step, while it holds the task's lease, and each change is
// one transaction. Everything a step causes — the new state, its journal
// entries, the decisions it recorded, the subtasks it created — commits
// together or not at all, so a crash can never leave a task between states.
type WorkflowRepository struct{ db *gorm.DB }

func NewWorkflowRepository(db *gorm.DB) *WorkflowRepository {
	return &WorkflowRepository{db: db}
}

var (
	// ErrLeaseLost means the caller no longer holds the task's lease: it
	// expired and another driver may have taken over. Nothing was written.
	ErrLeaseLost = errors.New("workflow: task lease is no longer held")
	// ErrTransitionConflict means the task is no longer in the state the caller
	// read before deciding what to do. Nothing was written; re-read and retry.
	ErrTransitionConflict = errors.New("workflow: task state changed since it was read")
)

// AcquireTaskLease takes the task's lease for owner if it is free or expired.
// A driver must hold the lease for the whole of one step.
func (q *WorkflowRepository) AcquireTaskLease(ctx context.Context, taskID int32, owner string, ttl time.Duration) (bool, error) {
	now := time.Now()
	result := q.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND (lease_owner = '' OR lease_until IS NULL OR lease_until < ?)", taskID, now).
		UpdateColumns(map[string]interface{}{"lease_owner": owner, "lease_until": now.Add(ttl)})
	return result.RowsAffected == 1, result.Error
}

// RenewTaskLease extends a lease the owner still holds; false means it was
// lost. A step that waits on a slow model call renews while it waits.
func (q *WorkflowRepository) RenewTaskLease(ctx context.Context, taskID int32, owner string, ttl time.Duration) (bool, error) {
	now := time.Now()
	result := q.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND lease_owner = ? AND lease_until >= ?", taskID, owner, now).
		UpdateColumn("lease_until", now.Add(ttl))
	return result.RowsAffected == 1, result.Error
}

// ReleaseTaskLease gives up a lease. Releasing one that was already lost is a
// no-op, not an error.
func (q *WorkflowRepository) ReleaseTaskLease(ctx context.Context, taskID int32, owner string) error {
	return q.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND lease_owner = ?", taskID, owner).
		UpdateColumns(map[string]interface{}{"lease_owner": "", "lease_until": nil}).Error
}

// startedTaskStatuses are the statuses of a task the workflow is responsible
// for moving: started and not yet finished.
var startedTaskStatuses = []string{TaskStatusTodo, TaskStatusDependsOnTask, TaskStatusInProgress, TaskStatusBlocked}

// ListTasksToAdvance returns started, unfinished tasks that no live driver
// holds. This is the sweeper's net: whatever wake-up was lost, a task in this
// list gets looked at again.
func (q *WorkflowRepository) ListTasksToAdvance(ctx context.Context, limit int) ([]int32, error) {
	var ids []int32
	query := q.db.WithContext(ctx).Model(&Task{}).
		Where("is_archived = ? AND status IN ?", false, startedTaskStatuses).
		Where("(lease_owner = '' OR lease_until IS NULL OR lease_until < ?)", time.Now()).
		Order("id")
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Pluck("id", &ids).Error
	return ids, err
}

// TransitionGuard names the task a transition changes and the conditions
// under which it may: the caller still holds the lease, and the task is still
// in the state the caller read before deciding.
type TransitionGuard struct {
	TaskID     int32
	LeaseOwner string
	Status     string
	Phase      string
	WaitingOn  string
}

// GuardFor builds the guard for a task as the caller just read it.
func GuardFor(task Task, leaseOwner string) TransitionGuard {
	return TransitionGuard{TaskID: task.ID, LeaseOwner: leaseOwner, Status: task.Status, Phase: task.Phase, WaitingOn: task.WaitingOn}
}

// WorkflowTransition runs fn as one transaction after checking the guard.
// fn receives a WorkflowTx bound to that transaction and performs every write
// of the step through it. A non-nil error from fn — or a failed guard — leaves
// the database exactly as it was.
//
// fn must not call a model, a tool or anything else slow: the database has a
// single connection, and it is held for the duration.
func (q *WorkflowRepository) WorkflowTransition(ctx context.Context, guard TransitionGuard, fn func(tx *WorkflowTx) error) error {
	if guard.LeaseOwner == "" {
		return errors.New("workflow: a transition requires the lease owner")
	}
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		// Touching the row first makes this transaction the task's writer for
		// its whole duration on PostgreSQL too (the row lock is held to commit).
		held := tx.Model(&Task{}).
			Where("id = ? AND lease_owner = ? AND lease_until >= ?", guard.TaskID, guard.LeaseOwner, now).
			UpdateColumn("updated_at", now)
		if held.Error != nil {
			return held.Error
		}
		if held.RowsAffected != 1 {
			return ErrLeaseLost
		}
		var task Task
		if err := tx.First(&task, guard.TaskID).Error; err != nil {
			return err
		}
		if task.Status != guard.Status || task.Phase != guard.Phase || task.WaitingOn != guard.WaitingOn {
			return fmt.Errorf("%w: task %d is %s/%s/%s, expected %s/%s/%s", ErrTransitionConflict, task.ID,
				task.Status, task.Phase, task.WaitingOn, guard.Status, guard.Phase, guard.WaitingOn)
		}
		return fn(&WorkflowTx{tx: tx, task: task, now: now})
	})
}

// WorkflowTx is the set of writes a workflow step may make, all inside the
// step's transaction.
type WorkflowTx struct {
	tx   *gorm.DB
	task Task
	now  time.Time
}

// Task is the guarded task as it stood when the transition began.
func (w *WorkflowTx) Task() Task { return w.task }

// UpdateTask changes columns of the guarded task. done_at follows status as
// it does everywhere else.
func (w *WorkflowTx) UpdateTask(fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	updates := make(map[string]interface{}, len(fields)+1)
	for column, value := range fields {
		updates[column] = value
	}
	if status, ok := fields["status"].(string); ok {
		switch {
		case status != TaskStatusDone:
			updates["done_at"] = nil
		case w.task.Status != TaskStatusDone || w.task.DoneAt == nil:
			updates["done_at"] = w.now
		}
	}
	return w.tx.Model(&Task{}).Where("id = ?", w.task.ID).Updates(updates).Error
}

// AppendStep adds an entry to a task's journal. A step with no task named
// belongs to the guarded task.
func (w *WorkflowTx) AppendStep(step TaskStep) (TaskStep, error) {
	if step.TaskID == 0 {
		step.TaskID = w.task.ID
		step.RootTaskID = w.task.RootTaskID
	}
	if step.CreatedAt.IsZero() {
		step.CreatedAt = w.now
	}
	err := w.tx.Create(&step).Error
	return step, err
}

// AddDecision records a decision made by this step. A decision with no task
// named belongs to the guarded task.
func (w *WorkflowTx) AddDecision(decision Decision) (Decision, error) {
	if decision.TaskID == 0 {
		decision.TaskID = w.task.ID
		decision.RootTaskID = w.task.RootTaskID
	}
	if decision.CreatedAt.IsZero() {
		decision.CreatedAt = w.now
	}
	err := w.tx.Create(&decision).Error
	return decision, err
}

// CreateSubtask creates a child of the guarded task together with its
// depends_on edges to earlier siblings. The edges are written directly: both
// ends are children of one parent created by one step, which is exactly what
// the relation checks would establish, and the caller has validated that the
// batch has no cycle.
func (w *WorkflowTx) CreateSubtask(child Task, dependsOn []int32) (Task, error) {
	parentID := w.task.ID
	child.ParentID = &parentID
	child.CompanyID = w.task.CompanyID
	child.SprintID = w.task.SprintID
	child.ProjectID = w.task.ProjectID
	if child.GitBaseBranch == "" {
		child.GitBaseBranch = w.task.GitBaseBranch
	}
	created, err := createTask(w.tx, child)
	if err != nil {
		return created, err
	}
	for _, prerequisiteID := range dependsOn {
		relation := TaskRelation{
			CompanyID:    created.CompanyID,
			SourceTaskID: created.ID,
			TargetTaskID: prerequisiteID,
			Kind:         TaskRelationDependsOn,
			CreatedAt:    w.now,
		}
		if err := w.tx.Create(&relation).Error; err != nil {
			return created, fmt.Errorf("link subtask %d to prerequisite %d: %w", created.ID, prerequisiteID, err)
		}
	}
	return created, nil
}

// RedirectDependents makes every task that depends on one task depend on
// another instead, leaving the tasks in except as they are. Both are children
// of the guarded task, and the new prerequisite was created by this step, so
// no cycle can come of it.
func (w *WorkflowTx) RedirectDependents(fromTaskID, toTaskID int32, except []int32) error {
	query := w.tx.Model(&TaskRelation{}).
		Where("target_task_id = ? AND kind = ?", fromTaskID, TaskRelationDependsOn)
	if len(except) > 0 {
		query = query.Where("source_task_id NOT IN ?", except)
	}
	return query.Update("target_task_id", toTaskID).Error
}

// CreateComment posts a comment as part of the step (a question to the human).
func (w *WorkflowTx) CreateComment(comment Comment) (Comment, error) {
	err := w.tx.Create(&comment).Error
	return comment, err
}

// CreateRun records the executor session a direct task is about to wait on.
func (w *WorkflowTx) CreateRun(run Run) (Run, error) {
	err := w.tx.Create(&run).Error
	return run, err
}

// ClaimWorkspace makes the guarded task the one writer of its tree's shared
// worktree. False means another task holds it.
func (w *WorkflowTx) ClaimWorkspace() (bool, error) {
	result := w.tx.Model(&Task{}).
		Where("id = ? AND (workspace_owner_task_id IS NULL OR workspace_owner_task_id = ?)", w.task.RootTaskID, w.task.ID).
		UpdateColumn("workspace_owner_task_id", w.task.ID)
	return result.RowsAffected == 1, result.Error
}

// ReleaseWorkspace gives the shared worktree back if the guarded task holds it.
func (w *WorkflowTx) ReleaseWorkspace() error {
	return w.tx.Model(&Task{}).
		Where("id = ? AND workspace_owner_task_id = ?", w.task.RootTaskID, w.task.ID).
		UpdateColumn("workspace_owner_task_id", nil).Error
}

// ReleaseLease ends the step's hold on the task as part of the transition, so
// the new state and the freed lease become visible together.
func (w *WorkflowTx) ReleaseLease() error {
	return w.tx.Model(&Task{}).Where("id = ?", w.task.ID).
		UpdateColumns(map[string]interface{}{"lease_owner": "", "lease_until": nil}).Error
}

// CancelDescendants marks every unfinished task beneath the guarded task
// canceled with the given reason, and returns their IDs. It is the one write a
// step makes to tasks other than its own or its new children: stopping a task
// must stop the work under it at once. A descendant's driver that was
// mid-step finds its guard no longer holds and writes nothing.
func (w *WorkflowTx) CancelDescendants(reason string) ([]int32, error) {
	var ids []int32
	err := w.tx.Raw(`WITH RECURSIVE subtree(id) AS (
  SELECT id FROM tasks WHERE parent_id = ?
  UNION ALL
  SELECT child.id FROM tasks AS child JOIN subtree ON child.parent_id = subtree.id
)
SELECT tasks.id FROM tasks JOIN subtree ON subtree.id = tasks.id
WHERE tasks.status NOT IN ?`, w.task.ID, []string{TaskStatusDone, TaskStatusFailed, TaskStatusCanceled}).Scan(&ids).Error
	if err != nil || len(ids) == 0 {
		return ids, err
	}
	err = w.tx.Model(&Task{}).Where("id IN ?", ids).Updates(map[string]interface{}{
		"status":        TaskStatusCanceled,
		"result_reason": reason,
		"phase":         "",
		"waiting_on":    TaskWaitNone,
		"wait_ref":      nil,
		"wait_until":    nil,
		"wait_detail":   "",
		"run_id":        nil,
		"done_at":       nil,
	}).Error
	if err != nil {
		return nil, err
	}
	// A canceled writer no longer holds the tree's worktree.
	err = w.tx.Model(&Task{}).
		Where("id = ? AND workspace_owner_task_id IN ?", w.task.RootTaskID, ids).
		UpdateColumn("workspace_owner_task_id", nil).Error
	return ids, err
}

// WorkflowChild is a subtask as its parent's workflow sees it: the task, the
// tool of the parent's step that created it, and the siblings it waits for.
type WorkflowChild struct {
	Task
	OriginTool string
	DependsOn  []int32
}

// ListWorkflowChildren returns the subtasks a task's own steps created, in the
// order they were created. Subtasks added by hand are not the workflow's to
// wait on and are left out.
func (q *WorkflowRepository) ListWorkflowChildren(ctx context.Context, parentID int32) ([]WorkflowChild, error) {
	var tasks []Task
	if err := q.db.WithContext(ctx).
		Where("parent_id = ? AND origin_step_id IS NOT NULL", parentID).Order("id").Find(&tasks).Error; err != nil {
		return nil, err
	}
	children := make([]WorkflowChild, len(tasks))
	if len(tasks) == 0 {
		return children, nil
	}
	ids := make([]int32, len(tasks))
	stepIDs := make([]int64, 0, len(tasks))
	index := make(map[int32]int, len(tasks))
	for i, task := range tasks {
		children[i].Task = task
		ids[i] = task.ID
		index[task.ID] = i
		stepIDs = append(stepIDs, *task.OriginStepID)
	}
	var steps []TaskStep
	if err := q.db.WithContext(ctx).Select("id", "tool_name").Where("id IN ?", stepIDs).Find(&steps).Error; err != nil {
		return nil, err
	}
	tools := make(map[int64]string, len(steps))
	for _, step := range steps {
		tools[step.ID] = step.ToolName
	}
	var relations []TaskRelation
	if err := q.db.WithContext(ctx).
		Where("source_task_id IN ? AND kind = ?", ids, TaskRelationDependsOn).Order("id").Find(&relations).Error; err != nil {
		return nil, err
	}
	for i := range children {
		children[i].OriginTool = tools[*children[i].OriginStepID]
	}
	for _, relation := range relations {
		i := index[relation.SourceTaskID]
		children[i].DependsOn = append(children[i].DependsOn, relation.TargetTaskID)
	}
	return children, nil
}

// ListTaskSubtree returns a task and everything beneath it, parents before
// their children.
func (q *WorkflowRepository) ListTaskSubtree(ctx context.Context, taskID int32) ([]Task, error) {
	var tasks []Task
	err := q.db.WithContext(ctx).Raw(`WITH RECURSIVE subtree(id) AS (
  SELECT id FROM tasks WHERE id = ?
  UNION ALL
  SELECT child.id FROM tasks AS child JOIN subtree ON child.parent_id = subtree.id
)
SELECT tasks.* FROM tasks JOIN subtree ON subtree.id = tasks.id ORDER BY tasks.depth, tasks.id`, taskID).Scan(&tasks).Error
	return tasks, err
}

// FindHumanAnswer returns the human's reply to the question a task waits on,
// or nil if there is none yet.
//
// A reply that names the question (reply_to_id) always counts. A plain comment
// on the root task counts too, but only for the oldest question still open in
// the tree and only once: with several questions open, an unaddressed comment
// answers the first and is never read as the answer to the others.
func (q *WorkflowRepository) FindHumanAnswer(ctx context.Context, task Task) (*Comment, error) {
	if task.WaitingOn != TaskWaitHuman || task.WaitRef == nil {
		return nil, nil
	}
	questionID := *task.WaitRef
	db := q.db.WithContext(ctx)

	var targeted Comment
	err := db.Where("reply_to_id = ? AND author_type = ?", questionID, "human").Order("id").First(&targeted).Error
	if err == nil {
		return &targeted, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var oldest *int32
	if err := db.Model(&Task{}).
		Where("root_task_id = ? AND waiting_on = ? AND wait_ref IS NOT NULL", task.RootTaskID, TaskWaitHuman).
		Select("MIN(wait_ref)").Scan(&oldest).Error; err != nil {
		return nil, err
	}
	if oldest == nil || *oldest != questionID {
		return nil, nil
	}
	var plain Comment
	err = db.Where("task_id = ? AND author_type = ? AND reply_to_id IS NULL AND id > ?", task.RootTaskID, "human", questionID).
		Where("(comment_type = '' OR comment_type IS NULL)").
		Where("NOT EXISTS (SELECT 1 FROM task_steps WHERE task_steps.kind = ? AND task_steps.comment_id = comments.id)", StepHumanAnswer).
		Order("id").First(&plain).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &plain, nil
}

// ListPrerequisites returns every task the given task depends on, whatever
// its status, in the order the dependencies were declared.
func (q *WorkflowRepository) ListPrerequisites(ctx context.Context, taskID int32) ([]Task, error) {
	var tasks []Task
	err := q.db.WithContext(ctx).
		Joins("JOIN task_relations tr ON tr.target_task_id = tasks.id").
		Where("tr.source_task_id = ? AND tr.kind = ?", taskID, TaskRelationDependsOn).
		Order("tr.id asc").Find(&tasks).Error
	return tasks, err
}

// ListTasksWaitingOn returns the IDs of the tasks in one tree that are in a
// given wait: the writers queued for the tree's worktree, for example.
func (q *WorkflowRepository) ListTasksWaitingOn(ctx context.Context, rootTaskID int32, wait string) ([]int32, error) {
	var ids []int32
	err := q.db.WithContext(ctx).Model(&Task{}).
		Where("root_task_id = ? AND waiting_on = ?", rootTaskID, wait).Order("id").Pluck("id", &ids).Error
	return ids, err
}

// ListAllTasksWaitingOn returns the IDs of every task, in any tree, that is
// in a given wait: those waiting for a vault to be unlocked, for example.
func (q *WorkflowRepository) ListAllTasksWaitingOn(ctx context.Context, wait string) ([]int32, error) {
	var ids []int32
	err := q.db.WithContext(ctx).Model(&Task{}).
		Where("waiting_on = ? AND is_archived = ?", wait, false).Order("id").Pluck("id", &ids).Error
	return ids, err
}
