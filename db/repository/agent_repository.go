package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type AgentRepository struct{ db *gorm.DB }

func NewAgentRepository(db *gorm.DB) *AgentRepository { return &AgentRepository{db: db} }
func (q *AgentRepository) CreateAgent(ctx context.Context, a Agent) (Agent, error) {
	err := q.db.WithContext(ctx).Create(&a).Error
	return a, err
}

func (q *AgentRepository) ListAgentsByCompany(ctx context.Context, companyID int32) ([]Agent, error) {
	var a []Agent
	err := q.db.WithContext(ctx).Where("company_id = ?", companyID).Order("id").Find(&a).Error
	return a, err
}

func (q *AgentRepository) GetAgent(ctx context.Context, id int32) (Agent, error) {
	var a Agent
	err := q.db.WithContext(ctx).First(&a, id).Error
	return a, err
}

func (q *AgentRepository) GetAgentWithCompany(ctx context.Context, id int32) (Agent, Company, error) {
	var a Agent
	err := q.db.WithContext(ctx).Preload("Company").First(&a, id).Error
	if err != nil {
		return Agent{}, Company{}, err
	}
	return a, a.Company, nil
}

func (q *AgentRepository) UpdateAgent(ctx context.Context, a Agent) (Agent, error) {
	err := q.db.WithContext(ctx).Save(&a).Error
	return a, err
}
