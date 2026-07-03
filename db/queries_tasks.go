package db

import "context"

func (q *Queries) CreateTask(ctx context.Context, t Task) (Task, error) {
	err := q.db.WithContext(ctx).Create(&t).Error
	return t, err
}

func (q *Queries) UpdateTask(ctx context.Context, t Task) (Task, error) {
	err := q.db.WithContext(ctx).Save(&t).Error
	return t, err
}

func (q *Queries) GetTask(ctx context.Context, id int32) (Task, error) {
	var t Task
	err := q.db.WithContext(ctx).Preload("Company").Preload("Project").Preload("Sprint").First(&t, id).Error
	return t, err
}

func (q *Queries) LockTaskRun(ctx context.Context, taskID int32, runID int32) error {
	return q.db.WithContext(ctx).Model(&Task{}).Where("id = ? AND run_id IS NULL", taskID).Update("run_id", runID).Error
}

func (q *Queries) UnlockTaskRun(ctx context.Context, taskID int32) error {
	return q.db.WithContext(ctx).Model(&Task{}).Where("id = ?", taskID).Update("run_id", nil).Error
}

// GetRootTask walks the parent chain from taskID and returns the top-most
// ancestor (the task itself when it has no parent). Bounded to 20 hops to
// guard against cycles.
func (q *Queries) GetRootTask(ctx context.Context, taskID int32) (Task, error) {
	task, err := q.GetTask(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	for hops := 0; task.ParentID != nil && hops < 20; hops++ {
		parent, err := q.GetTask(ctx, *task.ParentID)
		if err != nil {
			return task, nil // parent missing — treat current as root
		}
		task = parent
	}
	return task, nil
}

// UpdateTaskRefinedDescription stores the agent-produced refinement without
// touching the user's original description.
func (q *Queries) UpdateTaskRefinedDescription(ctx context.Context, taskID int32, refined string) error {
	return q.db.WithContext(ctx).Model(&Task{}).Where("id = ?", taskID).
		Update("refined_description", refined).Error
}
