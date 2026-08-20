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

func (q *AgentRepository) DeleteAgent(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Delete(&Agent{}, id).Error
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

// EnsureBuiltinAgentsForCompany creates newly introduced built-in roles and
// marks legacy rows with the same stable role key as built-in. Existing rows
// are not overwritten: after bootstrap, the database row is authoritative.
func (q *AgentRepository) EnsureBuiltinAgentsForCompany(ctx context.Context, companyID int32, defaults []Agent, providerID *int32, model string) error {
	var configured Agent
	if err := q.db.WithContext(ctx).
		Where("company_id = ? AND (provider_id IS NOT NULL OR model_group_id IS NOT NULL)", companyID).
		Order("id").First(&configured).Error; err != nil {
		configured = Agent{}
	}
	if providerID == nil && configured.ProviderID != nil {
		providerID = configured.ProviderID
	}
	if model == "" {
		model = configured.Model
	}
	if providerID == nil {
		var company Company
		if err := q.db.WithContext(ctx).First(&company, companyID).Error; err == nil && company.UserID != nil {
			var provider LLMProvider
			if err := q.db.WithContext(ctx).
				Where("user_id = ? AND enabled = ?", *company.UserID, true).
				Order("id").First(&provider).Error; err == nil {
				providerID = &provider.ID
				if model == "" {
					model = provider.DefaultModel
				}
			}
		}
	}

	for _, seed := range defaults {
		var existing Agent
		err := q.db.WithContext(ctx).
			Where("company_id = ? AND LOWER(role_key) = LOWER(?)", companyID, seed.RoleKey).
			First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			seed.CompanyID = companyID
			seed.ProviderID = providerID
			seed.Model = model
			if err := q.db.WithContext(ctx).Create(&seed).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		updates := map[string]interface{}{"builtin": true}
		if existing.RoleKey == "" {
			updates["role_key"] = seed.RoleKey
		}
		if err := q.db.WithContext(ctx).Model(&existing).Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}
