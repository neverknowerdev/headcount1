package db

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Slug of the model group seeded automatically on startup.
const DefaultMemoryGroupSlug = "memory-management"

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

// EnsureDefaultModelGroups seeds the built-in "Memory Management" group with
// the free gpt-oss* models currently listed on the builtin OpenRouter /
// OpenCode Zen providers. Idempotent: creates the group if missing, and
// (re)populates its members only while it has none, so user edits are never
// overwritten. Called after the free-model catalog refresh on startup.
func (q *Queries) EnsureDefaultModelGroups(ctx context.Context) error {
	var group ModelGroup
	err := q.db.WithContext(ctx).Where("slug = ?", DefaultMemoryGroupSlug).First(&group).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return err
		}
		group = ModelGroup{
			Name:        "Memory Management",
			Slug:        DefaultMemoryGroupSlug,
			Description: "Free gpt-oss models for lightweight memory-management calls, with automatic failover.",
			Builtin:     true,
		}
		if err := q.db.WithContext(ctx).Create(&group).Error; err != nil {
			return err
		}
	}

	var memberCount int64
	if err := q.db.WithContext(ctx).Model(&ModelGroupMember{}).Where("group_id = ?", group.ID).Count(&memberCount).Error; err != nil {
		return err
	}
	if memberCount > 0 {
		return nil
	}

	providers, err := q.ListLLMProviders(ctx)
	if err != nil {
		return err
	}

	var members []ModelGroupMember
	for _, p := range providers {
		if !p.Builtin || p.SupportedModels == "" {
			continue
		}
		for _, m := range strings.Split(p.SupportedModels, ",") {
			m = strings.TrimSpace(m)
			if m == "" || !strings.Contains(strings.ToLower(m), "gpt-oss") {
				continue
			}
			members = append(members, ModelGroupMember{
				GroupID:    group.ID,
				ProviderID: p.ID,
				Model:      m,
				IsFree:     true,
			})
		}
	}
	if len(members) == 0 {
		return nil
	}
	return q.ReplaceModelGroupMembers(ctx, group.ID, members)
}
