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
