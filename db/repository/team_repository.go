package repository

import (
	"context"
	"strings"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type TeamRepository struct{ db *gorm.DB }

func NewTeamRepository(db *gorm.DB) *TeamRepository { return &TeamRepository{db: db} }

// CreateTeamWithOwner creates a team and its owner membership in one
// transaction — the shape every non-invited registration produces.
func (q *TeamRepository) CreateTeamWithOwner(ctx context.Context, name string, ownerUserID int32) (Team, error) {
	team := Team{Name: name}
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&team).Error; err != nil {
			return err
		}
		return tx.Create(&TeamMember{TeamID: team.ID, UserID: ownerUserID, Role: TeamRoleOwner}).Error
	})
	return team, err
}

// EnsureTeamForUser gives a user their own team if they have no membership at
// all — idempotent; used at startup for accounts predating teams.
func (q *TeamRepository) EnsureTeamForUser(ctx context.Context, user User) error {
	var n int64
	if err := q.db.WithContext(ctx).Model(&TeamMember{}).Where("user_id = ?", user.ID).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := q.CreateTeamWithOwner(ctx, defaultTeamName(user.Email), user.ID)
	return err
}

// defaultTeamName derives the initial team name from the owner's email.
func defaultTeamName(email string) string {
	local, _, _ := strings.Cut(email, "@")
	if local == "" {
		local = email
	}
	return local + "'s team"
}

func (q *TeamRepository) GetTeam(ctx context.Context, id int32) (Team, error) {
	var team Team
	err := q.db.WithContext(ctx).First(&team, id).Error
	return team, err
}

func (q *TeamRepository) UpdateTeamName(ctx context.Context, id int32, name string) error {
	return q.db.WithContext(ctx).Model(&Team{}).Where("id = ?", id).Update("name", name).Error
}
