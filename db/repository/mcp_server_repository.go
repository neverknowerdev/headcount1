package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"log"
	"os/exec"
	"strings"
)

type MCPServerRepository struct{ db *gorm.DB }

func NewMCPServerRepository(db *gorm.DB) *MCPServerRepository {
	return &MCPServerRepository{db: db}
}
func (q *MCPServerRepository) CreateMCPServer(ctx context.Context, s MCPServer) (MCPServer, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}
func (q *MCPServerRepository) ListMCPServers(ctx context.Context, companyID, userID int32) ([]MCPServer, error) {
	var servers []MCPServer
	db := q.db.WithContext(ctx).Order("id").Preload("Project")
	if userID > 0 {
		accessibleCodegraph := "project_id IN (" + "SELECT p.id FROM projects p JOIN companies c ON c.id = p.company_id WHERE " + "c.team_id IN (SELECT team_id FROM team_members WHERE user_id = ?) OR " + "(c.team_id IS NULL AND c.user_id = ?))"
		db = db.Preload("Accounts", "user_id = ?", userID).Where("builtin = ? OR owner_user_id = ? OR ("+accessibleCodegraph+")", true, userID, userID, userID)
	} else {
		db = db.Preload("Accounts")
	}
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
			servers[i].Accounts[j].HasToken = servers[i].Accounts[j].AuthTokenEncrypted != ""
		}
		if servers[i].Transport != "builtin" && servers[i].ProjectID == nil {
			servers[i].Enabled = len(servers[i].Accounts) > 0
		}
		cmd := servers[i].Command
		genericRuntimes := map[string]bool{"npx": true, "node": true, "python3": true, "python": true, "deno": true, "bun": true}
		if cmd != "" && !genericRuntimes[cmd] {
			_, err := exec.LookPath(cmd)
			servers[i].DepsInstalled = err == nil
		} else {
			servers[i].DepsInstalled = cmd == ""
		}
	}
	return servers, nil
}
func (q *MCPServerRepository) GetMCPServer(ctx context.Context, id int32) (MCPServer, error) {
	var s MCPServer
	err := q.db.WithContext(ctx).Preload("Accounts").First(&s, id).Error
	return s, err
}
func (q *MCPServerRepository) UpdateMCPServer(ctx context.Context, s MCPServer) (MCPServer, error) {
	err := q.db.WithContext(ctx).Save(&s).Error
	return s, err
}
func (q *MCPServerRepository) DeleteMCPServer(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Delete(&MCPServer{}, id).Error
}

type CodegraphProjectServer struct {
	Project Project
	Server  MCPServer
}

func (q *MCPServerRepository) ListCodegraphProjectServers(ctx context.Context, companyID int32) ([]CodegraphProjectServer, error) {
	var servers []MCPServer
	err := q.db.WithContext(ctx).Where("project_id IS NOT NULL AND init_status = 'ready'").Find(&servers).Error
	if err != nil {
		return nil, err
	}
	var result []CodegraphProjectServer
	for _, s := range servers {
		var proj Project
		if err := q.db.WithContext(ctx).Where("id = ? AND company_id = ?", *s.ProjectID, companyID).First(&proj).Error; err != nil {
			continue
		}
		result = append(result, CodegraphProjectServer{Project: proj, Server: s})
	}
	return result, nil
}
func (q *MCPServerRepository) RepairOrphanedCodegraphServers(ctx context.Context) error {
	var servers []MCPServer
	if err := q.db.WithContext(ctx).Where("project_id IS NULL AND name LIKE 'codegraph-%'").Find(&servers).Error; err != nil {
		return err
	}
	for _, s := range servers {
		var projectID int32
		if _, err := fmt.Sscanf(s.Name, "codegraph-%d", &projectID); err != nil || projectID == 0 {
			continue
		}
		var proj Project
		if err := q.db.WithContext(ctx).First(&proj, projectID).Error; err != nil {
			continue
		}
		q.db.WithContext(ctx).Model(&MCPServer{}).Where("id = ?", s.ID).Update("project_id", projectID)
		log.Printf("codegraph repair: linked server %d (%s) to project %d", s.ID, s.Name, projectID)
	}
	return nil
}
func (q *MCPServerRepository) ListPendingCodegraphServers(ctx context.Context) ([]CodegraphProjectServer, error) {
	var servers []MCPServer
	err := q.db.WithContext(ctx).Where("project_id IS NOT NULL AND init_status != 'ready'").Find(&servers).Error
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
func (q *MCPServerRepository) UpdateMCPServerToolsCache(ctx context.Context, id int32, toolsJSON string) error {
	return q.db.WithContext(ctx).Model(&MCPServer{}).Where("id = ?", id).Updates(map[string]any{"tools_cache": toolsJSON, "last_error": ""}).Error
}
func (q *MCPServerRepository) UpdateMCPServerLastError(ctx context.Context, id int32, errMsg string) error {
	return q.db.WithContext(ctx).Model(&MCPServer{}).Where("id = ?", id).Update("last_error", errMsg).Error
}
func (q *MCPServerRepository) UpdateMCPServerInitStatus(ctx context.Context, id int32, status string) error {
	return q.db.WithContext(ctx).Model(&MCPServer{}).Where("id = ?", id).Update("init_status", status).Error
}
func (q *MCPServerRepository) GetCodegraphServerForProject(ctx context.Context, projectID int32) (MCPServer, error) {
	var s MCPServer
	err := q.db.WithContext(ctx).Where("project_id = ?", projectID).First(&s).Error
	return s, err
}
func (q *MCPServerRepository) EnsureBuiltinMCPServers(ctx context.Context) error {
	predefined := []MCPServer{{Name: MCPServerNameGitHub, DisplayName: "GitHub", Description: "Access GitHub repos, issues, pull requests, and code search.", Transport: "stdio", Command: "github-mcp-server", Args: `["stdio"]`, AuthType: MCPAuthTypeGitHubApp, AuthEnvVar: "GITHUB_PERSONAL_ACCESS_TOKEN", Enabled: false, Builtin: true}, {Name: "google-docs", DisplayName: "Google Docs", Description: "Read and write Google Docs, Sheets, and Drive files via Google Drive API. Requires OAuth2 authorization.", Transport: "stdio", Command: "npx", Args: `["-y", "@modelcontextprotocol/server-gdrive"]`, Deps: `["@modelcontextprotocol/server-gdrive"]`, AuthType: "google-oauth", AuthEnvVar: "GDRIVE_CREDENTIALS_PATH", Enabled: false, Builtin: true}, {Name: "postiz", DisplayName: "Postiz", Description: "Schedule and publish social media posts across platforms via Postiz.", Transport: "http", URL: "https://mcp.postiz.com/mcp", AuthType: "url-token", AuthEnvVar: "", Enabled: false, Builtin: true}, {Name: "brave-search", DisplayName: "Brave Search", Description: "Search the web, images, videos, news and local places via Brave Search API.", Transport: "stdio", Command: "npx", Args: `["-y", "@brave/brave-search-mcp-server"]`, Deps: `["@brave/brave-search-mcp-server"]`, AuthType: "bearer", AuthEnvVar: "BRAVE_API_KEY", Enabled: false, Builtin: true}}
	for _, s := range predefined {
		var existing MCPServer
		if q.db.WithContext(ctx).Where("name = ?", s.Name).First(&existing).Error == nil {
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
func (q *MCPServerRepository) ListAllMCPNpmDeps(ctx context.Context) ([]string, error) {
	var rows []MCPServer
	if err := q.db.WithContext(ctx).Where("deps != '' AND deps IS NOT NULL").Select("deps").Find(&rows).Error; err != nil {
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
func (q *MCPServerRepository) MigrateAddProjectFKToMCPServers(ctx context.Context) error {
	if q.db.Dialector.Name() != "sqlite" {
		return nil
	}
	var ddl string
	q.db.WithContext(ctx).Raw(`SELECT COALESCE(sql,'') FROM sqlite_master WHERE type='table' AND name='mcp_servers'`).Scan(&ddl)
	lower := strings.ToLower(ddl)
	if strings.Contains(lower, "references") && strings.Contains(lower, "projects") {
		return nil
	}
	if err := q.db.WithContext(ctx).Where("project_id IS NOT NULL AND project_id NOT IN (SELECT id FROM projects)").Delete(&MCPServer{}).Error; err != nil {
		return fmt.Errorf("purge orphans: %w", err)
	}
	return q.db.WithContext(ctx).Migrator().CreateConstraint(&MCPServer{}, "Project")
}
