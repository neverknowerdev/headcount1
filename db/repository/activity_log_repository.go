package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type ActivityLogRepository struct{ db *gorm.DB }

func NewActivityLogRepository(db *gorm.DB) *ActivityLogRepository {
	return &ActivityLogRepository{db: db}
}
func (r *ActivityLogRepository) ListAllActivityLogs(ctx context.Context) ([]ActivityLog, error) {
	var logs []ActivityLog
	err := r.db.WithContext(ctx).Order("id").Find(&logs).Error
	return logs, err
}
