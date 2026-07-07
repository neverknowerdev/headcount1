package db

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Slugs of the model groups seeded automatically on startup. Both are
// built-in: they can be edited (renamed, members changed) but never deleted.
const (
	DefaultMemoryGroupSlug  = "memory-management"
	DefaultUtilityGroupSlug = "utility"
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

// defaultModelGroupSpecs describes the built-in model groups seeded on
// startup: slug, display name, and description. Both default to "any model"
// from the free OpenRouter provider (see ensureDefaultGroupMember) so they
// work immediately without a live model-catalog fetch.
var defaultModelGroupSpecs = []struct {
	slug, name, description string
}{
	{
		DefaultMemoryGroupSlug, "Memory Management",
		"Free models for lightweight memory-management calls, with automatic failover.",
	},
	{
		DefaultUtilityGroupSlug, "Utility",
		"Cheap model used for internal one-shot calls (artifact Q&A, commit messages), with automatic failover.",
	},
}

// EnsureDefaultModelGroups seeds the built-in "Memory Management" and
// "Utility" model groups. Idempotent and safe to call on every startup:
// creates each group if missing (never deleted — Builtin groups are
// undeletable, see DeleteModelGroup), and adds a default "any model from
// OpenRouter" member only while the group has none, so user edits are never
// overwritten.
func (q *Queries) EnsureDefaultModelGroups(ctx context.Context) error {
	for _, spec := range defaultModelGroupSpecs {
		group, err := q.ensureModelGroup(ctx, spec.slug, spec.name, spec.description)
		if err != nil {
			return err
		}
		if err := q.ensureDefaultGroupMember(ctx, group); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queries) ensureModelGroup(ctx context.Context, slug, name, description string) (ModelGroup, error) {
	var group ModelGroup
	err := q.db.WithContext(ctx).Where("slug = ?", slug).First(&group).Error
	if err == nil {
		return group, nil
	}
	if err != gorm.ErrRecordNotFound {
		return ModelGroup{}, err
	}
	group = ModelGroup{Name: name, Slug: slug, Description: description, Builtin: true}
	if err := q.db.WithContext(ctx).Create(&group).Error; err != nil {
		return ModelGroup{}, err
	}
	return group, nil
}

// ensureDefaultGroupMember adds a single "any model from OpenRouter" member
// to group when it has none. Matched by the stable ProviderName field (not
// the user-editable display Name), so it still finds the provider even if
// it's been renamed. A no-op if the OpenRouter provider row doesn't exist
// yet or the group already has members.
func (q *Queries) ensureDefaultGroupMember(ctx context.Context, group ModelGroup) error {
	var memberCount int64
	if err := q.db.WithContext(ctx).Model(&ModelGroupMember{}).Where("group_id = ?", group.ID).Count(&memberCount).Error; err != nil {
		return err
	}
	if memberCount > 0 {
		return nil
	}

	var provider LLMProvider
	if err := q.db.WithContext(ctx).Where("provider_name = ?", ProviderVendorOpenRouter).First(&provider).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}

	return q.ReplaceModelGroupMembers(ctx, group.ID, []ModelGroupMember{
		{ProviderID: provider.ID, AllModels: true, IsFree: true},
	})
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
