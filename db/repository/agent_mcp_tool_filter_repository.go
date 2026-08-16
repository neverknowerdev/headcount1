package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type AgentMCPToolFilterRepository struct{ db *gorm.DB }

func NewAgentMCPToolFilterRepository(db *gorm.DB) *AgentMCPToolFilterRepository {
	return &AgentMCPToolFilterRepository{db: db}
}
func (q *AgentMCPToolFilterRepository) GetAgentMCPToolFilters(ctx context.Context, agentID int32) (map[int32]map[string]bool, error) {
	var rows []AgentMCPToolFilter
	if err := q.db.WithContext(ctx).Where("agent_id = ?", agentID).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[int32]map[string]bool, len(rows))
	for _, r := range rows {
		if result[r.MCPServerID] == nil {
			result[r.MCPServerID] = make(map[string]bool)
		}
		result[r.MCPServerID][r.ToolName] = r.Enabled
	}
	return result, nil
}
func (q *AgentMCPToolFilterRepository) SetAgentMCPToolFilters(ctx context.Context, agentID int32, filters []AgentMCPToolFilter) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("agent_id = ?", agentID).Delete(&AgentMCPToolFilter{}).Error; err != nil {
			return err
		}
		for _, f := range filters {
			if err := tx.Exec("INSERT INTO agent_mcp_tool_filters (agent_id, mcp_server_id, tool_name, enabled) VALUES (?, ?, ?, ?)", agentID, f.MCPServerID, f.ToolName, f.Enabled).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
