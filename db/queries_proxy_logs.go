package db

import "context"

func (q *Queries) CreateProxyRequestLog(ctx context.Context, p ProxyRequestLog) (ProxyRequestLog, error) {
	err := q.db.WithContext(ctx).Create(&p).Error
	return p, err
}
