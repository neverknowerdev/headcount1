package db

import "context"

func (q *Queries) CreateCompany(ctx context.Context, name string) (Company, error) {
	c := Company{Name: name}
	err := q.db.WithContext(ctx).Create(&c).Error
	return c, err
}

func (q *Queries) ListCompanies(ctx context.Context) ([]Company, error) {
	var c []Company
	err := q.db.WithContext(ctx).Order("id").Find(&c).Error
	return c, err
}

// ListCompaniesForUser returns the companies the user can work with: every
// company of a team they belong to, plus team-less rows they created — the
// tenancy boundary for everything scoped under a company.
func (q *Queries) ListCompaniesForUser(ctx context.Context, userID int32) ([]Company, error) {
	var c []Company
	err := q.db.WithContext(ctx).
		Where("team_id IN (SELECT team_id FROM team_members WHERE user_id = ?) OR (team_id IS NULL AND user_id = ?)", userID, userID).
		Order("id").Find(&c).Error
	return c, err
}

func (q *Queries) GetCompany(ctx context.Context, id int32) (Company, error) {
	var c Company
	err := q.db.WithContext(ctx).First(&c, id).Error
	return c, err
}

// BackfillCompanyTeams assigns a team to any legacy company that predates the
// "every company belongs to a team" invariant: team_id is set from the
// creator's (user_id) team membership. The memory layer's tenant is derived
// from a company's team, so a null team_id would leave its memory untenanted.
// Idempotent — only touches rows where team_id IS NULL.
func (q *Queries) BackfillCompanyTeams(ctx context.Context) error {
	var comps []Company
	if err := q.db.WithContext(ctx).Where("team_id IS NULL AND user_id IS NOT NULL").Find(&comps).Error; err != nil {
		return err
	}
	for _, c := range comps {
		m, err := q.GetTeamMembership(ctx, *c.UserID)
		if err != nil {
			continue // creator has no membership (unexpected after EnsureTeamForUser) — skip
		}
		if err := q.db.WithContext(ctx).Model(&Company{}).Where("id = ?", c.ID).Update("team_id", m.TeamID).Error; err != nil {
			return err
		}
	}
	return nil
}
