package repository

import (
	"context"

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

// ExpandModelGroupMembers resolves a group's members to the concrete models
// it routes between. A member that stands for all of a provider's models
// stands for those of the group's kind: a group of System One models never
// reaches a language model, or the other way round.
func ExpandModelGroupMembers(group ModelGroup) []ModelGroupMember {
	kind := group.Kind
	if !IsModelKind(kind) {
		kind = ModelKindLLM
	}
	result := make([]ModelGroupMember, 0, len(group.Members))
	for _, member := range group.Members {
		if !member.AllModels {
			if ModelKind(member.Model) == kind {
				result = append(result, member)
			}
			continue
		}
		for _, model := range member.Provider.ModelsOfKind(kind) {
			expanded := member
			expanded.Model, expanded.AllModels = model, false
			result = append(result, expanded)
		}
	}
	return result
}
