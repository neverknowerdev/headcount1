package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type ProxyRequestLogRepository struct{ db *gorm.DB }

func NewProxyRequestLogRepository(db *gorm.DB) *ProxyRequestLogRepository {
	return &ProxyRequestLogRepository{db: db}
}
func (q *ProxyRequestLogRepository) CreateProxyRequestLog(ctx context.Context, p ProxyRequestLog) (ProxyRequestLog, error) {
	err := q.db.WithContext(ctx).Create(&p).Error
	return p, err
}
