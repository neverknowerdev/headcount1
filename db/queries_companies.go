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

func (q *Queries) GetCompany(ctx context.Context, id int32) (Company, error) {
	var c Company
	err := q.db.WithContext(ctx).First(&c, id).Error
	return c, err
}
