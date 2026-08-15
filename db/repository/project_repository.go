package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type ProjectRepository struct{ db *gorm.DB }

func NewProjectRepository(db *gorm.DB) *ProjectRepository { return &ProjectRepository{db: db} }
func (q *ProjectRepository) CreateProject(ctx context.Context, p Project) (Project, error) {
	err := q.db.WithContext(ctx).Create(&p).Error
	return p, err
}

func (q *ProjectRepository) GetProject(ctx context.Context, id int32) (Project, error) {
	var p Project
	err := q.db.WithContext(ctx).Preload("Company").First(&p, id).Error
	return p, err
}

func (q *ProjectRepository) UpdateProject(ctx context.Context, p Project) (Project, error) {
	err := q.db.WithContext(ctx).Save(&p).Error
	return p, err
}

func (q *ProjectRepository) ListProjectsByCompany(ctx context.Context, companyID int32) ([]Project, error) {
	var p []Project
	err := q.db.WithContext(ctx).Where("company_id = ?", companyID).Order("id").Find(&p).Error
	return p, err
}
