package repository

import (
	"context"
	"fmt"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type TeamInviteRepository struct{ db *gorm.DB }

func NewTeamInviteRepository(db *gorm.DB) *TeamInviteRepository { return &TeamInviteRepository{db: db} }

const TeamInviteLifetime = 7 * 24 * time.Hour

func (r *TeamInviteRepository) CreateTeamInvite(ctx context.Context, teamID int32, email, role, tokenHash string, invitedBy int32) (TeamInvite, error) {
	invite := TeamInvite{TeamID: teamID, Email: NormalizeEmail(email), Role: role, TokenHash: tokenHash, InvitedBy: invitedBy, ExpiresAt: time.Now().Add(TeamInviteLifetime)}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("team_id = ? AND email = ? AND accepted_at IS NULL", teamID, invite.Email).Delete(&TeamInvite{}).Error; err != nil {
			return err
		}
		return tx.Create(&invite).Error
	})
	return invite, err
}

func (r *TeamInviteRepository) ListPendingTeamInvites(ctx context.Context, teamID int32) ([]TeamInvite, error) {
	var invites []TeamInvite
	err := r.db.WithContext(ctx).Where("team_id = ? AND accepted_at IS NULL AND expires_at > ?", teamID, time.Now()).Order("id").Find(&invites).Error
	return invites, err
}

func (r *TeamInviteRepository) DeleteTeamInvite(ctx context.Context, teamID, inviteID int32) error {
	return r.db.WithContext(ctx).Where("id = ? AND team_id = ?", inviteID, teamID).Delete(&TeamInvite{}).Error
}

func (r *TeamInviteRepository) GetTeamInviteByTokenHash(ctx context.Context, tokenHash string) (TeamInvite, error) {
	var invite TeamInvite
	err := r.db.WithContext(ctx).Where("token_hash = ? AND accepted_at IS NULL AND expires_at > ?", tokenHash, time.Now()).First(&invite).Error
	if err != nil {
		return TeamInvite{}, fmt.Errorf("invalid or expired invite")
	}
	return invite, nil
}

func (r *TeamInviteRepository) AcceptTeamInvite(ctx context.Context, invite TeamInvite, userID int32) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		result := tx.Model(&TeamInvite{}).Where("id = ? AND accepted_at IS NULL", invite.ID).Update("accepted_at", &now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("invite already used")
		}
		return tx.Create(&TeamMember{TeamID: invite.TeamID, UserID: userID, Role: invite.Role}).Error
	})
}
