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

// CodegraphProjectServer pairs a Project with its auto-created codegraph MCP server.
type CodegraphProjectServer struct {
	Project Project
	Server  MCPServer
}

// ListCodegraphProjectServers returns all projects for a company that have an
// associated codegraph MCP server (ProjectID set on the server).
func (q *Queries) ListCodegraphProjectServers(ctx context.Context, companyID int32) ([]CodegraphProjectServer, error) {
	var servers []MCPServer
	err := q.db.WithContext(ctx).
		Where("project_id IS NOT NULL").
		Find(&servers).Error
	if err != nil {
		return nil, err
	}

	var result []CodegraphProjectServer
	for _, s := range servers {
		var proj Project
		if err := q.db.WithContext(ctx).
			Where("id = ? AND company_id = ?", *s.ProjectID, companyID).
			First(&proj).Error; err != nil {
			continue // project doesn't belong to this company; skip
		}
		result = append(result, CodegraphProjectServer{Project: proj, Server: s})
	}
	return result, nil
}

func (q *Queries) UpdateMCPServerToolsCache(ctx context.Context, id int32, toolsJSON string) error {
	return q.db.WithContext(ctx).Model(&MCPServer{}).Where("id = ?", id).Update("tools_cache", toolsJSON).Error
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

// EnsureBuiltinMCPServers creates all predefined MCP servers if they don't
// already exist. Safe to call on every startup.
func (q *Queries) EnsureBuiltinMCPServers(ctx context.Context) error {
	predefined := []MCPServer{
		{
			Name:        "paperclip2",
			DisplayName: "Paperclip2",
			Description: "Built-in tools: update task status and create subtasks for agents.",
			Transport:   "builtin",
			Enabled:     true,
			Builtin:     true,
		},
		{
			Name:        "github",
			DisplayName: "GitHub",
			Description: "Access GitHub repos, issues, pull requests, and code search. Requires github-mcp-server (brew install github/tap/github-mcp-server).",
			Transport:   "stdio",
			Command:     "github-mcp-server",
			Args:        `["stdio"]`,
			AuthType:    "bearer",
			AuthEnvVar:  "GITHUB_PERSONAL_ACCESS_TOKEN",
			Enabled:     false,
			Builtin:     true,
		},
		{
			Name:        "google-docs",
			DisplayName: "Google Docs",
			Description: "Read and write Google Docs, Sheets, and Drive files. Requires a Google service account credentials JSON file.",
			Transport:   "stdio",
			Command:     "npx",
			Args:        `["-y", "@modelcontextprotocol/server-gdrive"]`,
			AuthType:    "credentials-file",
			AuthEnvVar:  "GOOGLE_APPLICATION_CREDENTIALS",
			Enabled:     false,
			Builtin:     true,
		},
		{
			Name:        "mempalace",
			DisplayName: "MemPalace",
			Description: "Long-term semantic memory across agent runs — diary, search, and knowledge graph. Install: uv tool install mempalace",
			Transport:   "stdio",
			Command:     "mempalace-mcp",
			Args:        `[]`,
			Enabled:     true,
			Builtin:     true,
		},
	}

	for _, s := range predefined {
		var existing MCPServer
		if q.db.WithContext(ctx).Where("name = ?", s.Name).First(&existing).Error == nil {
			continue // already exists — don't overwrite user's config
		}
		if err := q.db.WithContext(ctx).Create(&s).Error; err != nil {
			return err
		}
	}
	return nil
}
