package db

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

func (q *Queries) CreateModelGroup(ctx context.Context, g ModelGroup) (ModelGroup, error) {
	err := q.db.WithContext(ctx).Create(&g).Error
	return g, err
}

func (q *Queries) GetModelGroup(ctx context.Context, id int32) (ModelGroup, error) {
	var g ModelGroup
	err := q.db.WithContext(ctx).
		Preload("Members", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).
		Preload("Members.Provider").
		First(&g, id).Error
	return g, err
}

// GetModelGroupByKey resolves a group by slug, or by numeric ID when the key
// is a number. Used by the gateway so proxy URLs can use either form.
func (q *Queries) GetModelGroupByKey(ctx context.Context, key string) (ModelGroup, error) {
	var g ModelGroup
	err := q.db.WithContext(ctx).
		Preload("Members", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).
		Preload("Members.Provider").
		Where("slug = ? OR CAST(id AS TEXT) = ?", key, key).
		First(&g).Error
	return g, err
}

// ListModelGroupsForUser returns only the user's model groups.
func (q *Queries) ListModelGroupsForUser(ctx context.Context, userID int32) ([]ModelGroup, error) {
	var groups []ModelGroup
	err := q.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Preload("Members", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).
		Preload("Members.Provider").
		Order("id").
		Find(&groups).Error
	return groups, err
}

func (q *Queries) ListModelGroups(ctx context.Context) ([]ModelGroup, error) {
	var groups []ModelGroup
	err := q.db.WithContext(ctx).
		Preload("Members", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).
		Preload("Members.Provider").
		Order("id").
		Find(&groups).Error
	return groups, err
}

func (q *Queries) UpdateModelGroup(ctx context.Context, g ModelGroup) (ModelGroup, error) {
	err := q.db.WithContext(ctx).Omit("Members").Save(&g).Error
	return g, err
}

func (q *Queries) DeleteModelGroup(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("group_id = ?", id).Delete(&ModelGroupMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&ModelGroup{}, id).Error
	})
}

// ReplaceModelGroupMembers swaps a group's full member list in one
// transaction. Member priority is set from slice order.
func (q *Queries) ReplaceModelGroupMembers(ctx context.Context, groupID int32, members []ModelGroupMember) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("group_id = ?", groupID).Delete(&ModelGroupMember{}).Error; err != nil {
			return err
		}
		for i := range members {
			members[i].ID = 0
			members[i].GroupID = groupID
			members[i].Priority = i
			if err := tx.Create(&members[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (q *Queries) CreateModelRequestStat(ctx context.Context, s ModelRequestStat) (ModelRequestStat, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

// ListModelRequestStatsSince returns stat rows newer than since, optionally
// filtered to one group. Aggregation (failure %, tokens/sec averages, time
// buckets) is done in Go by the caller so the query stays dialect-agnostic.
func (q *Queries) ListModelRequestStatsSince(ctx context.Context, groupID *int32, since time.Time) ([]ModelRequestStat, error) {
	var stats []ModelRequestStat
	tx := q.db.WithContext(ctx).Where("created_at >= ?", since)
	if groupID != nil {
		tx = tx.Where("group_id = ?", *groupID)
	}
	err := tx.Order("created_at").Find(&stats).Error
	return stats, err
}

// ExpandModelGroupMembers turns each AllModels member into one concrete
// member per model in its provider's SupportedModels, so routing always
// operates on concrete (provider, model) pairs that track the provider's
// live catalog. Regular (non-wildcard) members pass through unchanged.
// Callers must have preloaded Members.Provider.
func ExpandModelGroupMembers(members []ModelGroupMember) []ModelGroupMember {
	out := make([]ModelGroupMember, 0, len(members))
	for _, m := range members {
		if !m.AllModels {
			out = append(out, m)
			continue
		}
		for _, model := range strings.Split(m.Provider.SupportedModels, ",") {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			expanded := m
			expanded.Model = model
			expanded.AllModels = false
			out = append(out, expanded)
		}
	}
	return out
}
