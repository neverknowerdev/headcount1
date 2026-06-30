package db

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"

	"gorm.io/gorm"
)

func (q *Queries) CreateMCPServer(ctx context.Context, s MCPServer) (MCPServer, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

// ListMCPServers returns all MCP servers with their accounts preloaded.
// For non-builtin servers, Enabled is computed from account presence.
// When companyID > 0, codegraph servers (project_id IS NOT NULL) are filtered
// to only those whose project belongs to the given company.
func (q *Queries) ListMCPServers(ctx context.Context, companyID int32) ([]MCPServer, error) {
	var servers []MCPServer
	db := q.db.WithContext(ctx).Order("id").Preload("Accounts").Preload("Project")
	if companyID > 0 {
		db = db.Where("project_id IS NULL OR project_id IN (SELECT id FROM projects WHERE company_id = ?)", companyID)
	} else {
		db = db.Where("project_id IS NULL OR project_id IN (SELECT id FROM projects)")
	}
	err := db.Find(&servers).Error
	if err != nil {
		return nil, err
	}
	for i := range servers {
		for j := range servers[i].Accounts {
			servers[i].Accounts[j].HasToken = servers[i].Accounts[j].AuthToken != ""
		}
		// For non-builtin servers, Enabled reflects account presence — EXCEPT
		// for codegraph servers (ProjectID set) which are managed by init_status,
		// not by accounts.
		if servers[i].Transport != "builtin" && servers[i].ProjectID == nil {
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

// ListCodegraphProjectServers returns ready codegraph servers for a company.
// Only servers with init_status = 'ready' are returned so agent runs don't
// get an un-initialised knowledge graph.
func (q *Queries) ListCodegraphProjectServers(ctx context.Context, companyID int32) ([]CodegraphProjectServer, error) {
	var servers []MCPServer
	err := q.db.WithContext(ctx).
		Where("project_id IS NOT NULL AND init_status = 'ready'").
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

// RepairOrphanedCodegraphServers fixes codegraph servers whose project_id column
// is NULL but whose name encodes the project ID as "codegraph-{N}". This can
// happen when a server was created before the project_id column was added.
func (q *Queries) RepairOrphanedCodegraphServers(ctx context.Context) error {
	var servers []MCPServer
	if err := q.db.WithContext(ctx).
		Where("project_id IS NULL AND name LIKE 'codegraph-%'").
		Find(&servers).Error; err != nil {
		return err
	}
	for _, s := range servers {
		var projectID int32
		if _, err := fmt.Sscanf(s.Name, "codegraph-%d", &projectID); err != nil || projectID == 0 {
			continue
		}
		// Verify the project exists before linking.
		var proj Project
		if err := q.db.WithContext(ctx).First(&proj, projectID).Error; err != nil {
			continue
		}
		q.db.WithContext(ctx).Model(&MCPServer{}).Where("id = ?", s.ID).Update("project_id", projectID)
		log.Printf("codegraph repair: linked server %d (%s) to project %d", s.ID, s.Name, projectID)
	}
	return nil
}

// ListPendingCodegraphServers returns all codegraph servers (across all companies)
// whose init_status is not yet 'ready'. Used by the startup sweep.
func (q *Queries) ListPendingCodegraphServers(ctx context.Context) ([]CodegraphProjectServer, error) {
	var servers []MCPServer
	err := q.db.WithContext(ctx).
		Where("project_id IS NOT NULL AND init_status != 'ready'").
		Find(&servers).Error
	if err != nil {
		return nil, err
	}

	var result []CodegraphProjectServer
	for _, s := range servers {
		var proj Project
		if err := q.db.WithContext(ctx).First(&proj, s.ProjectID).Error; err != nil {
			continue
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

func (q *Queries) UpdateMCPServerInitStatus(ctx context.Context, id int32, status string) error {
	return q.db.WithContext(ctx).Model(&MCPServer{}).Where("id = ?", id).Update("init_status", status).Error
}

// GetCodegraphServerForProject returns the auto-created codegraph MCP server for a project.
func (q *Queries) GetCodegraphServerForProject(ctx context.Context, projectID int32) (MCPServer, error) {
	var s MCPServer
	err := q.db.WithContext(ctx).Where("project_id = ?", projectID).First(&s).Error
	return s, err
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

// GetAgentCodegraphAssignments returns a map of codegraph server ID → enabled for a given agent.
// Servers with no row default to enabled.
func (q *Queries) GetAgentCodegraphAssignments(ctx context.Context, agentID int32) (map[int32]bool, error) {
	var rows []AgentMCPServer
	err := q.db.WithContext(ctx).
		Joins("JOIN mcp_servers ON mcp_servers.id = agent_mcp_servers.mcp_server_id").
		Where("agent_mcp_servers.agent_id = ? AND mcp_servers.project_id IS NOT NULL", agentID).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make(map[int32]bool, len(rows))
	for _, r := range rows {
		result[r.MCPServerID] = r.Enabled
	}
	return result, nil
}

// SetAgentCodegraphAssignments replaces all codegraph server assignments for an agent.
// Uses raw SQL for the INSERT so GORM's zero-value skipping doesn't drop Enabled=false.
func (q *Queries) SetAgentCodegraphAssignments(ctx context.Context, agentID int32, assignments []AgentMCPServer) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`DELETE FROM agent_mcp_servers WHERE agent_id = ? AND mcp_server_id IN (SELECT id FROM mcp_servers WHERE project_id IS NOT NULL)`,
			agentID,
		).Error; err != nil {
			return err
		}
		for _, a := range assignments {
			if err := tx.Exec(
				`INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, enabled) VALUES (?, ?, ?)`,
				agentID, a.MCPServerID, a.Enabled,
			).Error; err != nil {
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
			Description: "Access GitHub repos, issues, pull requests, and code search.",
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
			Deps:        `["@modelcontextprotocol/server-gdrive"]`,
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
		{
			Name:        "brave-search",
			DisplayName: "Brave Search",
			Description: "Search the web, images, videos, news and local places via Brave Search API.",
			Transport:   "stdio",
			Command:     "npx",
			Args:        `["-y", "@brave/brave-search-mcp-server"]`,
			Deps:        `["@brave/brave-search-mcp-server"]`,
			AuthType:    "bearer",
			AuthEnvVar:  "BRAVE_API_KEY",
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
			if existing.Deps != s.Deps {
				updates["deps"] = s.Deps
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

// ListAllMCPNpmDeps returns the deduplicated set of npm packages declared in
// the Deps field across all MCP servers.
func (q *Queries) ListAllMCPNpmDeps(ctx context.Context) ([]string, error) {
	var rows []MCPServer
	if err := q.db.WithContext(ctx).
		Where("deps != '' AND deps IS NOT NULL").
		Select("deps").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var result []string
	for _, row := range rows {
		var pkgs []string
		if err := json.Unmarshal([]byte(row.Deps), &pkgs); err != nil {
			log.Printf("db: invalid deps JSON for MCP server: %v", err)
			continue
		}
		for _, pkg := range pkgs {
			pkg = strings.TrimSpace(pkg)
			if pkg != "" && !seen[pkg] {
				seen[pkg] = true
				result = append(result, pkg)
			}
		}
	}
	return result, nil
}

// MigrateAddProjectFKToMCPServers adds a proper FK constraint from
// mcp_servers.project_id → projects.id with ON DELETE CASCADE.
// SQLite cannot ALTER TABLE ADD CONSTRAINT, so the migrator rebuilds the table.
// Idempotent: skips if the constraint is already present in the DDL.
func (q *Queries) MigrateAddProjectFKToMCPServers(ctx context.Context) error {
	// Only needed for SQLite — PostgreSQL already enforces via the FK tag on AutoMigrate.
	if q.db.Dialector.Name() != "sqlite" {
		return nil
	}
	var ddl string
	q.db.WithContext(ctx).Raw(
		`SELECT COALESCE(sql,'') FROM sqlite_master WHERE type='table' AND name='mcp_servers'`,
	).Scan(&ddl)
	lower := strings.ToLower(ddl)
	if strings.Contains(lower, "references") && strings.Contains(lower, "projects") {
		return nil // FK already present (glebarez uses backtick-quoted identifiers)
	}
	// Purge orphans first — the new FK would reject them.
	if err := q.db.WithContext(ctx).
		Where("project_id IS NOT NULL AND project_id NOT IN (SELECT id FROM projects)").
		Delete(&MCPServer{}).Error; err != nil {
		return fmt.Errorf("purge orphans: %w", err)
	}
	// GORM's SQLite migrator implements CreateConstraint via the 12-step table rebuild.
	return q.db.WithContext(ctx).Migrator().CreateConstraint(&MCPServer{}, "Project")
}

// ── AgentMCPToolFilter ───────────────────────────────────────────────────────

// GetAgentMCPToolFilters returns all tool filter rows for a given agent,
// grouped as serverID → toolName → enabled.
func (q *Queries) GetAgentMCPToolFilters(ctx context.Context, agentID int32) (map[int32]map[string]bool, error) {
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

// SetAgentMCPToolFilters replaces all tool filter rows for a given agent.
func (q *Queries) SetAgentMCPToolFilters(ctx context.Context, agentID int32, filters []AgentMCPToolFilter) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("agent_id = ?", agentID).Delete(&AgentMCPToolFilter{}).Error; err != nil {
			return err
		}
		for _, f := range filters {
			f.AgentID = agentID
			// Select("*") forces GORM to write all fields including Enabled=false,
			// which would otherwise be skipped as the zero value when the column has default:true.
			if err := tx.Select("*").Create(&f).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
