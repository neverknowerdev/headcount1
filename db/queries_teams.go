package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// TeamInviteLifetime bounds how long an emailed invite link works.
const TeamInviteLifetime = 7 * 24 * time.Hour

// CreateTeamWithOwner creates a team and its owner membership in one
// transaction — the shape every non-invited registration produces.
func (q *Queries) CreateTeamWithOwner(ctx context.Context, name string, ownerUserID int32) (Team, error) {
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
func (q *Queries) EnsureTeamForUser(ctx context.Context, user User) error {
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

// GetTeamMembership returns the user's (single, by current product rules)
// team membership.
func (q *Queries) GetTeamMembership(ctx context.Context, userID int32) (TeamMember, error) {
	var m TeamMember
	err := q.db.WithContext(ctx).Where("user_id = ?", userID).Order("id").First(&m).Error
	return m, err
}

// IsTeamMember reports whether the user belongs to the team.
func (q *Queries) IsTeamMember(ctx context.Context, teamID, userID int32) bool {
	var n int64
	q.db.WithContext(ctx).Model(&TeamMember{}).
		Where("team_id = ? AND user_id = ?", teamID, userID).Count(&n)
	return n > 0
}

func (q *Queries) GetTeam(ctx context.Context, id int32) (Team, error) {
	var t Team
	err := q.db.WithContext(ctx).First(&t, id).Error
	return t, err
}

func (q *Queries) UpdateTeamName(ctx context.Context, id int32, name string) error {
	return q.db.WithContext(ctx).Model(&Team{}).Where("id = ?", id).Update("name", name).Error
}

// TeamMemberInfo is the wire shape of one member row (user data joined in).
type TeamMemberInfo struct {
	UserID   int32     `json:"user_id"`
	Email    string    `json:"email"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

func (q *Queries) ListTeamMembers(ctx context.Context, teamID int32) ([]TeamMemberInfo, error) {
	var members []TeamMemberInfo
	err := q.db.WithContext(ctx).Model(&TeamMember{}).
		Select("team_members.user_id, users.email, team_members.role, team_members.created_at as joined_at").
		Joins("JOIN users ON users.id = team_members.user_id").
		Where("team_members.team_id = ?", teamID).
		Order("team_members.id").
		Scan(&members).Error
	return members, err
}

// ListTeamUserIDs returns the member user IDs — the fan-out set for
// tenant-scoped WebSocket events.
func (q *Queries) ListTeamUserIDs(ctx context.Context, teamID int32) ([]int32, error) {
	var ids []int32
	err := q.db.WithContext(ctx).Model(&TeamMember{}).
		Where("team_id = ?", teamID).Pluck("user_id", &ids).Error
	return ids, err
}

// ── invites ──────────────────────────────────────────────────────────────────

// CreateTeamInvite stores the hash of a fresh invite token, replacing any
// still-pending invite for the same email in this team.
func (q *Queries) CreateTeamInvite(ctx context.Context, teamID int32, email, role, tokenHash string, invitedBy int32) (TeamInvite, error) {
	inv := TeamInvite{
		TeamID:    teamID,
		Email:     NormalizeEmail(email),
		Role:      role,
		TokenHash: tokenHash,
		InvitedBy: invitedBy,
		ExpiresAt: time.Now().Add(TeamInviteLifetime),
	}
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("team_id = ? AND email = ? AND accepted_at IS NULL", teamID, inv.Email).
			Delete(&TeamInvite{}).Error; err != nil {
			return err
		}
		return tx.Create(&inv).Error
	})
	return inv, err
}

// ListPendingTeamInvites returns unaccepted, unexpired invites for a team.
func (q *Queries) ListPendingTeamInvites(ctx context.Context, teamID int32) ([]TeamInvite, error) {
	var invites []TeamInvite
	err := q.db.WithContext(ctx).
		Where("team_id = ? AND accepted_at IS NULL AND expires_at > ?", teamID, time.Now()).
		Order("id").Find(&invites).Error
	return invites, err
}

func (q *Queries) DeleteTeamInvite(ctx context.Context, teamID, inviteID int32) error {
	return q.db.WithContext(ctx).
		Where("id = ? AND team_id = ?", inviteID, teamID).Delete(&TeamInvite{}).Error
}

// GetTeamInviteByTokenHash returns a pending, unexpired invite.
func (q *Queries) GetTeamInviteByTokenHash(ctx context.Context, tokenHash string) (TeamInvite, error) {
	var inv TeamInvite
	err := q.db.WithContext(ctx).
		Where("token_hash = ? AND accepted_at IS NULL AND expires_at > ?", tokenHash, time.Now()).
		First(&inv).Error
	if err != nil {
		return TeamInvite{}, fmt.Errorf("invalid or expired invite")
	}
	return inv, nil
}

// AcceptTeamInvite atomically burns the invite and adds the user to the team
// with the invited role.
func (q *Queries) AcceptTeamInvite(ctx context.Context, invite TeamInvite, userID int32) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		res := tx.Model(&TeamInvite{}).
			Where("id = ? AND accepted_at IS NULL", invite.ID).
			Update("accepted_at", &now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("invite already used")
		}
		return tx.Create(&TeamMember{TeamID: invite.TeamID, UserID: userID, Role: invite.Role}).Error
	})
}
