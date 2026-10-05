package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type SprintRepository struct{ db *gorm.DB }

func NewSprintRepository(db *gorm.DB) *SprintRepository { return &SprintRepository{db: db} }
func (q *SprintRepository) CreateSprint(ctx context.Context, s Sprint) (Sprint, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

func (q *SprintRepository) ListSprintsByCompany(ctx context.Context, companyID int32) ([]Sprint, error) {
	var s []Sprint
	err := q.db.WithContext(ctx).Where("company_id = ?", companyID).Order("id").Find(&s).Error
	return s, err
}
