package db

import "context"

func (q *Queries) CreateAttachment(ctx context.Context, a Attachment) (Attachment, error) {
	err := q.db.WithContext(ctx).Create(&a).Error
	return a, err
}

func (q *Queries) ListAttachmentsByTask(ctx context.Context, taskID int32) ([]Attachment, error) {
	var a []Attachment
	err := q.db.WithContext(ctx).Where("task_id = ?", taskID).Order("created_at asc").Find(&a).Error
	return a, err
}
