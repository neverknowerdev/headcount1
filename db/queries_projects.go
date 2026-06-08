package db

import "context"

func (q *Queries) CreateProject(ctx context.Context, p Project) (Project, error) {
	err := q.db.WithContext(ctx).Create(&p).Error
	return p, err
}

func (q *Queries) GetProject(ctx context.Context, id int32) (Project, error) {
	var p Project
	err := q.db.WithContext(ctx).Preload("Company").First(&p, id).Error
	return p, err
}

func (q *Queries) UpdateProject(ctx context.Context, p Project) (Project, error) {
	err := q.db.WithContext(ctx).Save(&p).Error
	return p, err
}

func (q *Queries) ListProjectsByCompany(ctx context.Context, companyID int32) ([]Project, error) {
	var p []Project
	err := q.db.WithContext(ctx).Where("company_id = ?", companyID).Order("id").Find(&p).Error
	return p, err
}
