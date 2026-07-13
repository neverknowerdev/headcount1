package db

import "context"

func (q *Queries) CreateSystemLLMLog(ctx context.Context, l SystemLLMLog) (SystemLLMLog, error) {
	err := q.db.WithContext(ctx).Create(&l).Error
	return l, err
}

// ListSystemLLMLogs returns system LLM calls newest-first, optionally
// filtered by source, with the total count for pagination.
func (q *Queries) ListSystemLLMLogs(ctx context.Context, source string, limit, offset int) ([]SystemLLMLog, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	tx := q.db.WithContext(ctx).Model(&SystemLLMLog{})
	if source != "" {
		tx = tx.Where("source = ?", source)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var logs []SystemLLMLog
	err := tx.Order("id DESC").Limit(limit).Offset(offset).Find(&logs).Error
	return logs, total, err
}

func (q *Queries) GetSystemLLMLog(ctx context.Context, id int32) (SystemLLMLog, error) {
	var l SystemLLMLog
	err := q.db.WithContext(ctx).First(&l, id).Error
	return l, err
}
