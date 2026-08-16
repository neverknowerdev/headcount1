package repository

import (
	"context"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type ModelRequestStatRepository struct{ db *gorm.DB }

func NewModelRequestStatRepository(db *gorm.DB) *ModelRequestStatRepository {
	return &ModelRequestStatRepository{db: db}
}

func (r *ModelRequestStatRepository) CreateModelRequestStat(ctx context.Context, stat ModelRequestStat) (ModelRequestStat, error) {
	err := r.db.WithContext(ctx).Create(&stat).Error
	return stat, err
}

func (r *ModelRequestStatRepository) ListModelRequestStatsSince(ctx context.Context, groupID *int32, since time.Time) ([]ModelRequestStat, error) {
	var stats []ModelRequestStat
	query := r.db.WithContext(ctx).Where("created_at >= ?", since)
	if groupID != nil {
		query = query.Where("group_id = ?", *groupID)
	}
	err := query.Order("created_at").Find(&stats).Error
	return stats, err
}
