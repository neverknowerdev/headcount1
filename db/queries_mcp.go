package db

import (
	"context"
	"log"
	"os/exec"

	"gorm.io/gorm"
)

func (q *Queries) CreateMCPServer(ctx context.Context, s MCPServer) (MCPServer, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

// ListMCPServers returns all MCP servers with their accounts preloaded.
// For non-builtin servers, Enabled is computed from account presence.
func (q *Queries) ListMCPServers(ctx context.Context) ([]MCPServer, error) {
	var servers []MCPServer
	err := q.db.WithContext(ctx).Order("id").Preload("Accounts").Find(&servers).Error
	if err != nil {
		return nil, err
	}
	for i := range servers {
		for j := range servers[i].Accounts {
			servers[i].Accounts[j].HasToken = servers[i].Accounts[j].AuthToken != ""
		}
		if servers[i].Transport != "builtin" {
			servers[i].Enabled = len(servers[i].Accounts) > 0
		}
		// Check whether a dedicated CLI binary is installed.
		// Generic runtimes (npx, node, python3) don't count — only purpose-built binaries do.
		cmd := servers[i].Command
		genericRuntimes := map[string]bool{"npx": true, "node": true, "python3": true, "python": true, "deno": true, "bun": true}
		if cmd != "" && !genericRuntimes[cmd] {
			_, err := exec.LookPath(cmd)
			servers[i].DepsInstalled = err == nil
		} else {
			servers[i].DepsInstalled = cmd == "" // HTTP/builtin servers have no binary to install
		}
	}
	return servers, nil
}

func (q *Queries) GetMCPServer(ctx context.Context, id int32) (MCPServer, error) {
	var s MCPServer
	err := q.db.WithContext(ctx).Preload("Accounts").First(&s, id).Error
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
	return q.db.WithContext(ctx).Model(&MCPServer{}).Where("id = ?", id).
		Updates(map[string]any{"tools_cache": toolsJSON, "last_error": ""}).Error
}

func (q *Queries) UpdateMCPServerLastError(ctx context.Context, id int32, errMsg string) error {
	return q.db.WithContext(ctx).Model(&MCPServer{}).Where("id = ?", id).Update("last_error", errMsg).Error
}

// ── MCPToolStat ──────────────────────────────────────────────────────────────

func (q *Queries) IncrementMCPToolCallCount(ctx context.Context, serverID int32, toolName string) error {
	return q.db.WithContext(ctx).Exec(
		`INSERT INTO mcp_tool_stats (mcp_server_id, tool_name, call_count) VALUES (?, ?, 1)
		 ON CONFLICT (mcp_server_id, tool_name) DO UPDATE SET call_count = call_count + 1`,
		serverID, toolName,
	).Error
}

func (q *Queries) GetMCPToolCallCounts(ctx context.Context, serverID int32) (map[string]int64, error) {
	var stats []MCPToolStat
	if err := q.db.WithContext(ctx).Where("mcp_server_id = ?", serverID).Find(&stats).Error; err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(stats))
	for _, s := range stats {
		result[s.ToolName] = s.CallCount
	}
	return result, nil
}

// ── MCPAccount CRUD ──────────────────────────────────────────────────────────

func (q *Queries) CreateMCPAccount(ctx context.Context, a MCPAccount) (MCPAccount, error) {
	err := q.db.WithContext(ctx).Create(&a).Error
	a.HasToken = a.AuthToken != ""
	return a, err
}

func (q *Queries) GetMCPAccount(ctx context.Context, id int32) (MCPAccount, error) {
	var a MCPAccount
	err := q.db.WithContext(ctx).First(&a, id).Error
	a.HasToken = a.AuthToken != ""
	return a, err
}

func (q *Queries) UpdateMCPAccount(ctx context.Context, a MCPAccount) (MCPAccount, error) {
	err := q.db.WithContext(ctx).Save(&a).Error
	a.HasToken = a.AuthToken != ""
	return a, err
}

func (q *Queries) DeleteMCPAccount(ctx context.Context, id int32) error {
	// Remove agent assignments first.
	if err := q.db.WithContext(ctx).Where("mcp_account_id = ?", id).Delete(&AgentMCPAccount{}).Error; err != nil {
		return err
	}
	return q.db.WithContext(ctx).Delete(&MCPAccount{}, id).Error
}

func (q *Queries) UpdateMCPAccountLastError(ctx context.Context, id int32, errMsg string) error {
	return q.db.WithContext(ctx).Model(&MCPAccount{}).Where("id = ?", id).Update("last_error", errMsg).Error
}

// ListMCPAccountsForServer returns all accounts for a given server.
func (q *Queries) ListMCPAccountsForServer(ctx context.Context, serverID int32) ([]MCPAccount, error) {
	var accounts []MCPAccount
	err := q.db.WithContext(ctx).Where("mcp_server_id = ?", serverID).Find(&accounts).Error
	for i := range accounts {
		accounts[i].HasToken = accounts[i].AuthToken != ""
	}
	return accounts, err
}

// ── Agent-account assignments ─────────────────────────────────────────────────

// ListMCPAccountsForAgent returns accounts enabled for this agent, with server info preloaded.
// Used by the engine to create MCP clients with the right credentials.
func (q *Queries) ListMCPAccountsForAgent(ctx context.Context, agentID int32) ([]MCPAccount, error) {
	var accounts []MCPAccount
	err := q.db.WithContext(ctx).
		Joins("JOIN agent_mcp_accounts ON agent_mcp_accounts.mcp_account_id = mcp_accounts.id").
		Where("agent_mcp_accounts.agent_id = ? AND agent_mcp_accounts.enabled = ?", agentID, true).
		Find(&accounts).Error
	return accounts, err
}

// ListAllAgentMCPAccountAssignments returns all AgentMCPAccount rows for a given agent.
func (q *Queries) ListAllAgentMCPAccountAssignments(ctx context.Context, agentID int32) ([]AgentMCPAccount, error) {
	var rows []AgentMCPAccount
	err := q.db.WithContext(ctx).Where("agent_id = ?", agentID).Find(&rows).Error
	return rows, err
}

// SetAgentMCPAccounts replaces all account assignments for an agent.
func (q *Queries) SetAgentMCPAccounts(ctx context.Context, agentID int32, assignments []AgentMCPAccount) error {
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

// ListMCPServersForAgent returns all MCP servers enabled for the given agent (legacy, builtin only).
func (q *Queries) ListMCPServersForAgent(ctx context.Context, agentID int32) ([]MCPServer, error) {
	var servers []MCPServer
	err := q.db.WithContext(ctx).
		Joins("JOIN agent_mcp_servers ON agent_mcp_servers.mcp_server_id = mcp_servers.id").
		Where("agent_mcp_servers.agent_id = ? AND agent_mcp_servers.enabled = ?", agentID, true).
		Where("mcp_servers.enabled = ?", true).
		Find(&servers).Error
	return servers, err
}

// ListAllAgentMCPAssignments returns all AgentMCPServer rows for a given agent.
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

// MigrateServerTokensToAccounts converts any legacy auth_token on MCPServer rows
// into MCPAccount("Default") records, and migrates AgentMCPServer → AgentMCPAccount.
// Safe to run on every startup (idempotent).
func (q *Queries) MigrateServerTokensToAccounts(ctx context.Context) error {
	// Find servers that still have a legacy auth_token.
	type serverRow struct {
		ID        int32
		AuthToken string
	}
	var rows []serverRow
	if err := q.db.WithContext(ctx).Raw("SELECT id, auth_token FROM mcp_servers WHERE auth_token != ''").Scan(&rows).Error; err != nil {
		return err
	}

	for _, row := range rows {
		// Skip if already migrated.
		var count int64
		q.db.WithContext(ctx).Model(&MCPAccount{}).Where("mcp_server_id = ?", row.ID).Count(&count)
		if count > 0 {
			q.db.WithContext(ctx).Exec("UPDATE mcp_servers SET auth_token = '' WHERE id = ?", row.ID)
			continue
		}

		acc := MCPAccount{MCPServerID: row.ID, Name: "Default", AuthToken: row.AuthToken}
		if err := q.db.WithContext(ctx).Create(&acc).Error; err != nil {
			log.Printf("MCP migration: failed to create account for server %d: %v", row.ID, err)
			continue
		}
		// Clear the legacy token.
		q.db.WithContext(ctx).Exec("UPDATE mcp_servers SET auth_token = '', enabled = true WHERE id = ?", row.ID)

		// Migrate AgentMCPServer → AgentMCPAccount for this server.
		var agentAssigns []AgentMCPServer
		q.db.WithContext(ctx).Where("mcp_server_id = ?", row.ID).Find(&agentAssigns)
		for _, a := range agentAssigns {
			ama := AgentMCPAccount{AgentID: a.AgentID, MCPAccountID: acc.ID, Enabled: a.Enabled}
			q.db.WithContext(ctx).Create(&ama)
		}
		log.Printf("MCP migration: migrated server %d auth_token → account %d", row.ID, acc.ID)
	}
	return nil
}

// EnsureBuiltinMCPServers creates all predefined MCP servers if they don't
// already exist. Safe to call on every startup.
func (q *Queries) EnsureBuiltinMCPServers(ctx context.Context) error {
	predefined := []MCPServer{
		{
			Name:        "github",
			DisplayName: "GitHub",
			Description: "Access GitHub repos, issues, pull requests, and code search. Auto-installs via brew on first use.",
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
			Description: "Read and write Google Docs, Sheets, and Drive files via Google Drive API. Requires OAuth2 authorization.",
			Transport:   "stdio",
			Command:     "npx",
			Args:        `["-y", "@modelcontextprotocol/server-gdrive"]`,
			AuthType:    "google-oauth",
			AuthEnvVar:  "GDRIVE_CREDENTIALS_PATH",
			Enabled:     false,
			Builtin:     true,
		},
		{
			Name:        "postiz",
			DisplayName: "Postiz",
			Description: "Schedule and publish social media posts across platforms via Postiz.",
			Transport:   "http",
			URL:         "https://mcp.postiz.com/mcp",
			AuthType:    "url-token",
			AuthEnvVar:  "",
			Enabled:     false,
			Builtin:     true,
		},
	}

	for _, s := range predefined {
		var existing MCPServer
		if q.db.WithContext(ctx).Where("name = ?", s.Name).First(&existing).Error == nil {
			// Update mutable metadata fields that may change between versions.
			updates := map[string]any{}
			if existing.AuthType != s.AuthType {
				updates["auth_type"] = s.AuthType
			}
			if existing.AuthEnvVar != s.AuthEnvVar {
				updates["auth_env_var"] = s.AuthEnvVar
			}
			if existing.Description != s.Description {
				updates["description"] = s.Description
			}
			if s.URL != "" && existing.URL != s.URL {
				updates["url"] = s.URL
			}
			if existing.AuthEnvVar != s.AuthEnvVar {
				updates["auth_env_var"] = s.AuthEnvVar
			}
			if len(updates) > 0 {
				q.db.WithContext(ctx).Model(&existing).Updates(updates)
			}
			continue
		}
		if err := q.db.WithContext(ctx).Create(&s).Error; err != nil {
			return err
		}
	}
	return nil
}
