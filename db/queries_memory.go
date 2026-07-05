package db

import (
	"context"
	"time"
)

// CreateMemoryActivity inserts one memory-activity row. Query/Args/Response
// text are truncated so freeform content can't bloat the table.
func (q *Queries) CreateMemoryActivity(ctx context.Context, a MemoryActivity) (MemoryActivity, error) {
	if len(a.Query) > 500 {
		a.Query = a.Query[:500]
	}
	if len(a.Args) > 4000 {
		a.Args = a.Args[:4000]
	}
	if len(a.Response) > 8000 {
		a.Response = a.Response[:8000]
	}
	err := q.db.WithContext(ctx).Create(&a).Error
	return a, err
}

// ListMemoryActivity returns activity rows for a company, newest first, with
// optional agent/kind filters and offset paging. Args/Response are omitted —
// the list is meant to stay light; fetch GetMemoryActivity for the full log.
func (q *Queries) ListMemoryActivity(ctx context.Context, companyID int32, agentName, kind string, limit, offset int) ([]MemoryActivity, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	tx := q.db.WithContext(ctx).Model(&MemoryActivity{}).Where("company_id = ?", companyID)
	if agentName != "" {
		tx = tx.Where("agent_name = ?", agentName)
	}
	if kind != "" {
		tx = tx.Where("kind = ?", kind)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []MemoryActivity
	err := tx.Omit("args", "response").Order("id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

// GetMemoryActivity returns one activity row (with Args/Response) for the
// Activity tab's "view full log" detail, scoped to a company.
func (q *Queries) GetMemoryActivity(ctx context.Context, companyID, id int32) (MemoryActivity, error) {
	var row MemoryActivity
	err := q.db.WithContext(ctx).Where("company_id = ? AND id = ?", companyID, id).First(&row).Error
	return row, err
}

// MemoryAgentStat aggregates memory usage per agent for the Agents tab.
type MemoryAgentStat struct {
	AgentName    string     `json:"agent_name"`
	Reads        int64      `json:"reads"`
	Writes       int64      `json:"writes"`
	LastID       int32      `json:"-"`
	LastActivity *time.Time `json:"last_activity"`
}

// MemoryAgentStats returns per-agent read/write counts and last activity for
// a company. Engine/UI-initiated rows (empty agent name) are excluded.
// MAX(created_at) scans inconsistently across SQLite/Postgres drivers, so the
// newest row id is aggregated instead and its timestamp resolved separately.
func (q *Queries) MemoryAgentStats(ctx context.Context, companyID int32) ([]MemoryAgentStat, error) {
	var stats []MemoryAgentStat
	err := q.db.WithContext(ctx).Model(&MemoryActivity{}).
		Select(`agent_name,
			SUM(CASE WHEN kind = 'read' THEN 1 ELSE 0 END) AS reads,
			SUM(CASE WHEN kind = 'write' THEN 1 ELSE 0 END) AS writes,
			MAX(id) AS last_id`).
		Where("company_id = ? AND agent_name != ''", companyID).
		Group("agent_name").
		Order("MAX(id) DESC").
		Scan(&stats).Error
	if err != nil {
		return nil, err
	}
	ids := make([]int32, 0, len(stats))
	for _, s := range stats {
		ids = append(ids, s.LastID)
	}
	if len(ids) > 0 {
		var rows []MemoryActivity
		if err := q.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err == nil {
			byID := make(map[int32]time.Time, len(rows))
			for _, r := range rows {
				byID[r.ID] = r.CreatedAt
			}
			for i := range stats {
				if t, ok := byID[stats[i].LastID]; ok {
					ts := t
					stats[i].LastActivity = &ts
				}
			}
		}
	}
	return stats, nil
}

// PurgeOldMemoryActivity deletes activity rows older than the retention
// window (90 days). Called from the daily scheduler.
func (q *Queries) PurgeOldMemoryActivity(ctx context.Context) error {
	cutoff := time.Now().AddDate(0, 0, -90)
	return q.db.WithContext(ctx).Where("created_at < ?", cutoff).Delete(&MemoryActivity{}).Error
}
