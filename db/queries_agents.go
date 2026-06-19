package db

import "context"

func (q *Queries) CreateAgent(ctx context.Context, a Agent) (Agent, error) {
	err := q.db.WithContext(ctx).Create(&a).Error
	return a, err
}

func (q *Queries) ListAgentsByCompany(ctx context.Context, companyID int32) ([]Agent, error) {
	var a []Agent
	err := q.db.WithContext(ctx).Where("company_id = ?", companyID).Order("id").Find(&a).Error
	return a, err
}

func (q *Queries) GetAgent(ctx context.Context, id int32) (Agent, error) {
	var a Agent
	err := q.db.WithContext(ctx).First(&a, id).Error
	return a, err
}

func (q *Queries) GetAgentWithCompany(ctx context.Context, id int32) (Agent, Company, error) {
	var a Agent
	err := q.db.WithContext(ctx).Preload("Company").First(&a, id).Error
	if err != nil {
		return Agent{}, Company{}, err
	}
	return a, a.Company, nil
}

func (q *Queries) UpdateAgent(ctx context.Context, a Agent) (Agent, error) {
	err := q.db.WithContext(ctx).Save(&a).Error
	return a, err
}
