package db

import (
	"context"

	"gorm.io/gorm"
)

func (q *Queries) CreateMCPServer(ctx context.Context, s MCPServer) (MCPServer, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

// ListMCPServers returns all MCP servers (global, not company-scoped).
func (q *Queries) ListMCPServers(ctx context.Context) ([]MCPServer, error) {
	var servers []MCPServer
	err := q.db.WithContext(ctx).Order("id").Find(&servers).Error
	return servers, err
}

func (q *Queries) GetMCPServer(ctx context.Context, id int32) (MCPServer, error) {
	var s MCPServer
	err := q.db.WithContext(ctx).First(&s, id).Error
	return s, err
}

func (q *Queries) UpdateMCPServer(ctx context.Context, s MCPServer) (MCPServer, error) {
	err := q.db.WithContext(ctx).Save(&s).Error
	return s, err
}

func (q *Queries) DeleteMCPServer(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Delete(&MCPServer{}, id).Error
}

// ListMCPServersForAgent returns all MCP servers enabled for the given agent.
func (q *Queries) ListMCPServersForAgent(ctx context.Context, agentID int32) ([]MCPServer, error) {
	var servers []MCPServer
	err := q.db.WithContext(ctx).
		Joins("JOIN agent_mcp_servers ON agent_mcp_servers.mcp_server_id = mcp_servers.id").
		Where("agent_mcp_servers.agent_id = ? AND agent_mcp_servers.enabled = ?", agentID, true).
		Where("mcp_servers.enabled = ?", true).
		Find(&servers).Error
	return servers, err
}

// ListAllAgentMCPAssignments returns all AgentMCPServer rows for a given agent
// (both enabled and disabled), used by the UI.
func (q *Queries) ListAllAgentMCPAssignments(ctx context.Context, agentID int32) ([]AgentMCPServer, error) {
	var rows []AgentMCPServer
	err := q.db.WithContext(ctx).Where("agent_id = ?", agentID).Find(&rows).Error
	return rows, err
}

// SetAgentMCPServers replaces all MCP server assignments for an agent.
func (q *Queries) SetAgentMCPServers(ctx context.Context, agentID int32, assignments []AgentMCPServer) error {
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

// EnsureBuiltinMCPServer creates the paperclip2 built-in MCP server if it
// does not yet exist. It is global (not company-scoped).
func (q *Queries) EnsureBuiltinMCPServer(ctx context.Context) (MCPServer, error) {
	var existing MCPServer
	err := q.db.WithContext(ctx).
		Where("name = ? AND builtin = ?", "paperclip2", true).
		First(&existing).Error
	if err == nil {
		return existing, nil
	}
	s := MCPServer{
		Name:        "paperclip2",
		DisplayName: "Paperclip2",
		Description: "Built-in Paperclip2 tools: update task status and create subtasks.",
		Transport:   "builtin",
		Enabled:     true,
		Builtin:     true,
	}
	err = q.db.WithContext(ctx).Create(&s).Error
	return s, err
}
