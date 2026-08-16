package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type AgentMCPServerRepository struct{ db *gorm.DB }

func NewAgentMCPServerRepository(db *gorm.DB) *AgentMCPServerRepository {
	return &AgentMCPServerRepository{db: db}
}
func (q *AgentMCPServerRepository) GetAgentCodegraphAssignments(ctx context.Context, agentID int32) (map[int32]bool, error) {
	var rows []AgentMCPServer
	err := q.db.WithContext(ctx).Joins("JOIN mcp_servers ON mcp_servers.id = agent_mcp_servers.mcp_server_id").Where("agent_mcp_servers.agent_id = ? AND mcp_servers.project_id IS NOT NULL", agentID).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make(map[int32]bool, len(rows))
	for _, r := range rows {
		result[r.MCPServerID] = r.Enabled
	}
	return result, nil
}
func (q *AgentMCPServerRepository) SetAgentCodegraphAssignments(ctx context.Context, agentID int32, assignments []AgentMCPServer) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM agent_mcp_servers WHERE agent_id = ? AND mcp_server_id IN (SELECT id FROM mcp_servers WHERE project_id IS NOT NULL)`, agentID).Error; err != nil {
			return err
		}
		for _, a := range assignments {
			if err := tx.Exec(`INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, enabled) VALUES (?, ?, ?)`, agentID, a.MCPServerID, a.Enabled).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func (q *AgentMCPServerRepository) ListMCPServersForAgent(ctx context.Context, agentID int32) ([]MCPServer, error) {
	var servers []MCPServer
	err := q.db.WithContext(ctx).Joins("JOIN agent_mcp_servers ON agent_mcp_servers.mcp_server_id = mcp_servers.id").Where("agent_mcp_servers.agent_id = ? AND agent_mcp_servers.enabled = ?", agentID, true).Where("mcp_servers.enabled = ?", true).Find(&servers).Error
	return servers, err
}
func (q *AgentMCPServerRepository) ListAllAgentMCPAssignments(ctx context.Context, agentID int32) ([]AgentMCPServer, error) {
	var rows []AgentMCPServer
	err := q.db.WithContext(ctx).Where("agent_id = ?", agentID).Find(&rows).Error
	return rows, err
}
func (q *AgentMCPServerRepository) SetAgentMCPServers(ctx context.Context, agentID int32, assignments []AgentMCPServer) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("agent_id = ?", agentID).Delete(&AgentMCPServer{}).Error; err != nil {
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
