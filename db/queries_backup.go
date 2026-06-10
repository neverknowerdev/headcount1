package db

import "context"

func (q *Queries) ListAllTasks(ctx context.Context) ([]Task, error) {
	var tasks []Task
	err := q.db.WithContext(ctx).Order("id").Find(&tasks).Error
	return tasks, err
}

func (q *Queries) ListAllRuns(ctx context.Context) ([]Run, error) {
	var runs []Run
	err := q.db.WithContext(ctx).Order("id").Find(&runs).Error
	return runs, err
}

func (q *Queries) ListAllSkills(ctx context.Context) ([]Skill, error) {
	var skills []Skill
	err := q.db.WithContext(ctx).Order("id").Find(&skills).Error
	return skills, err
}

func (q *Queries) ListAllActivityLogs(ctx context.Context) ([]ActivityLog, error) {
	var logs []ActivityLog
	err := q.db.WithContext(ctx).Order("id").Find(&logs).Error
	return logs, err
}

func (q *Queries) ListAllComments(ctx context.Context) ([]Comment, error) {
	var comments []Comment
	err := q.db.WithContext(ctx).Order("id").Find(&comments).Error
	return comments, err
}

func (q *Queries) ListAllAttachments(ctx context.Context) ([]Attachment, error) {
	var attachments []Attachment
	err := q.db.WithContext(ctx).Order("id").Find(&attachments).Error
	return attachments, err
}

func (q *Queries) DeleteAllFromTable(ctx context.Context, tableName string) error {
	return q.db.WithContext(ctx).Exec("DELETE FROM " + tableName).Error
}
