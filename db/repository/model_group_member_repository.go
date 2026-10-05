package repository

import (
	"context"
	"strings"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type ModelGroupMemberRepository struct{ db *gorm.DB }

func NewModelGroupMemberRepository(db *gorm.DB) *ModelGroupMemberRepository {
	return &ModelGroupMemberRepository{db: db}
}

func (r *ModelGroupMemberRepository) ReplaceModelGroupMembers(ctx context.Context, groupID int32, members []ModelGroupMember) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("group_id = ?", groupID).Delete(&ModelGroupMember{}).Error; err != nil {
			return err
		}
		for i := range members {
			members[i].ID, members[i].GroupID, members[i].Priority = 0, groupID, i
			if err := tx.Create(&members[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func ExpandModelGroupMembers(members []ModelGroupMember) []ModelGroupMember {
	result := make([]ModelGroupMember, 0, len(members))
	for _, member := range members {
		if !member.AllModels {
			result = append(result, member)
			continue
		}
		for _, model := range strings.Split(member.Provider.SupportedModels, ",") {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			expanded := member
			expanded.Model, expanded.AllModels = model, false
			result = append(result, expanded)
		}
	}
	return result
}
