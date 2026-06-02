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
