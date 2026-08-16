package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type AgentMCPAccountRepository struct{ db *gorm.DB }

func NewAgentMCPAccountRepository(db *gorm.DB) *AgentMCPAccountRepository {
	return &AgentMCPAccountRepository{db: db}
}
func (q *AgentMCPAccountRepository) ListAllAgentMCPAccountAssignments(ctx context.Context, agentID int32) ([]AgentMCPAccount, error) {
	var rows []AgentMCPAccount
	err := q.db.WithContext(ctx).Where("agent_id = ?", agentID).Find(&rows).Error
	return rows, err
}
func (q *AgentMCPAccountRepository) SetAgentMCPAccounts(ctx context.Context, agentID int32, assignments []AgentMCPAccount) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("agent_id = ?", agentID).Delete(&AgentMCPAccount{}).Error; err != nil {
			return err
		}
		for _, a := range assignments {
			a.AgentID = agentID
			if err := tx.Create(&a).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
