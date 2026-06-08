package db

import "context"

func (q *Queries) CreateComment(ctx context.Context, c Comment) (Comment, error) {
	err := q.db.WithContext(ctx).Create(&c).Error
	return c, err
}

func (q *Queries) ListCommentsByTask(ctx context.Context, taskID int32) ([]Comment, error) {
	var c []Comment
	err := q.db.WithContext(ctx).Where("task_id = ?", taskID).Order("created_at asc").Find(&c).Error
	return c, err
}
