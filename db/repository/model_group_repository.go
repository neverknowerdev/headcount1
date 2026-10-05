package repository

import (
	"context"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type ModelGroupRepository struct{ db *gorm.DB }

func NewModelGroupRepository(db *gorm.DB) *ModelGroupRepository { return &ModelGroupRepository{db: db} }

func (q *ModelGroupRepository) CreateModelGroup(ctx context.Context, g ModelGroup) (ModelGroup, error) {
	err := q.db.WithContext(ctx).Create(&g).Error
	return g, err
}

func (q *ModelGroupRepository) GetModelGroup(ctx context.Context, id int32) (ModelGroup, error) {
	var g ModelGroup
	err := q.db.WithContext(ctx).
		Preload("Members", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).
		Preload("Members.Provider").
		First(&g, id).Error
	return g, err
}

// GetModelGroupByKey resolves a group by slug, or by numeric ID when the key
// is a number. Used by the gateway so proxy URLs can use either form.
func (q *ModelGroupRepository) GetModelGroupByKey(ctx context.Context, key string) (ModelGroup, error) {
	var g ModelGroup
	err := q.db.WithContext(ctx).
		Preload("Members", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).
		Preload("Members.Provider").
		Where("slug = ? OR CAST(id AS TEXT) = ?", key, key).
		First(&g).Error
	return g, err
}

// ListModelGroupsForUser returns only the user's model groups.
func (q *ModelGroupRepository) ListModelGroupsForUser(ctx context.Context, userID int32) ([]ModelGroup, error) {
	var groups []ModelGroup
	err := q.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Preload("Members", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).
		Preload("Members.Provider").
		Order("id").
		Find(&groups).Error
	return groups, err
}

func (q *ModelGroupRepository) ListModelGroups(ctx context.Context) ([]ModelGroup, error) {
	var groups []ModelGroup
	err := q.db.WithContext(ctx).
		Preload("Members", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).
		Preload("Members.Provider").
		Order("id").
		Find(&groups).Error
	return groups, err
}

func (q *ModelGroupRepository) UpdateModelGroup(ctx context.Context, g ModelGroup) (ModelGroup, error) {
	err := q.db.WithContext(ctx).Omit("Members").Save(&g).Error
	return g, err
}

func (q *ModelGroupRepository) DeleteModelGroup(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("group_id = ?", id).Delete(&ModelGroupMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&ModelGroup{}, id).Error
	})
}

// ReplaceModelGroupMembers swaps a group's full member list in one
// transaction. Member priority is set from slice order.
