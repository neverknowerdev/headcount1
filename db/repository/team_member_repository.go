package repository

import (
	"context"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type TeamMemberRepository struct{ db *gorm.DB }

func NewTeamMemberRepository(db *gorm.DB) *TeamMemberRepository { return &TeamMemberRepository{db: db} }

type TeamMemberInfo struct {
	UserID   int32     `json:"user_id"`
	Email    string    `json:"email"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

func (r *TeamMemberRepository) GetTeamMembership(ctx context.Context, userID int32) (TeamMember, error) {
	var member TeamMember
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("id").First(&member).Error
	return member, err
}

func (r *TeamMemberRepository) IsTeamMember(ctx context.Context, teamID, userID int32) bool {
	var count int64
	r.db.WithContext(ctx).Model(&TeamMember{}).Where("team_id = ? AND user_id = ?", teamID, userID).Count(&count)
	return count > 0
}

func (r *TeamMemberRepository) ListTeamMembers(ctx context.Context, teamID int32) ([]TeamMemberInfo, error) {
	var members []TeamMemberInfo
	err := r.db.WithContext(ctx).Model(&TeamMember{}).
		Select("team_members.user_id, users.email, team_members.role, team_members.created_at as joined_at").
		Joins("JOIN users ON users.id = team_members.user_id").Where("team_members.team_id = ?", teamID).
		Order("team_members.id").Scan(&members).Error
	return members, err
}

func (r *TeamMemberRepository) ListTeamUserIDs(ctx context.Context, teamID int32) ([]int32, error) {
	var ids []int32
	err := r.db.WithContext(ctx).Model(&TeamMember{}).Where("team_id = ?", teamID).Pluck("user_id", &ids).Error
	return ids, err
}
