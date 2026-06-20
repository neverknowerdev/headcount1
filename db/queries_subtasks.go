package db

import "context"

// ListSubtasksByParent returns all tasks whose parent_id equals parentID,
// ordered by creation time ascending.
func (q *Queries) ListSubtasksByParent(ctx context.Context, parentID int32) ([]Task, error) {
	var tasks []Task
	err := q.db.WithContext(ctx).
		Preload("Agent").
		Where("parent_id = ?", parentID).
		Order("created_at ASC").
		Find(&tasks).Error
	return tasks, err
}

// CountRunningSubtasks returns the number of subtasks of parentID that currently
// have an active run (task.run_id points to a run with status = 'running').
func (q *Queries) CountRunningSubtasks(ctx context.Context, parentID int32) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).
		Model(&Task{}).
		Joins("JOIN runs ON runs.id = tasks.run_id").
		Where("tasks.parent_id = ? AND runs.status = ?", parentID, "running").
		Count(&count).Error
	return count, err
}
