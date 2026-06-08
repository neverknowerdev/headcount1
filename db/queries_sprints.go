package db

import "context"

func (q *Queries) CreateSprint(ctx context.Context, s Sprint) (Sprint, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

func (q *Queries) ListSprintsByCompany(ctx context.Context, companyID int32) ([]Sprint, error) {
	var s []Sprint
	err := q.db.WithContext(ctx).Where("company_id = ?", companyID).Order("id").Find(&s).Error
	return s, err
}
