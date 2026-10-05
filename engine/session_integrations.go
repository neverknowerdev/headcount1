package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/githubapp"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/secrets"
)

type sessionIntegrations struct {
	registry            *aicli.Registry
	systemPrompt        string
	listingCostTotal    int
	listingCostByServer map[string]int
	close               func()
}

func (e *NativeEngine) configureSessionIntegrations(
	ctx context.Context,
	task db.Task,
	agent db.Agent,
	registry *aicli.Registry,
	systemPrompt string,
	logger *logging.ProxyLogger,
	allCompanyMCP bool,
) sessionIntegrations {
	accountIDByName := make(map[string]int32)
	serverIDByName := make(map[string]int32)
	store := tools.NewMCPSessionStore(nil, func(serverName, rawErr string) {
		accountID, ok := accountIDByName[serverName]
		if !ok {
			return
		}
		message := "Auth token invalid or expired. Re-authenticate."
		if strings.HasPrefix(serverName, db.MCPServerNameGitHub+"/") {
			message = "GitHub App authorization failed. Check the app installation permissions and try again."
		}
		lowerError := strings.ToLower(rawErr)
		if strings.Contains(lowerError, "forbidden") || strings.Contains(lowerError, "permission denied") {
			message = "Permission denied. Check your auth token has the required scopes."
		}
		_ = e.q.UpdateMCPAccountLastError(context.Background(), accountID, message)
	}, func(serverName, toolName string) {
		if serverID, ok := serverIDByName[serverName]; ok {
			_ = e.q.IncrementMCPToolCallCount(context.Background(), serverID, toolName)
		}
	})

	var taskProject *db.Project
	if task.ProjectID != nil {
		if project, err := e.q.GetProject(ctx, *task.ProjectID); err == nil {
			taskProject = &project
		}
	}
	githubAccounts := make(map[string]db.MCPAccount)
	store.SetAuthTokenRefresher(func(refreshCtx context.Context, serverName string) (string, error) {
		account, ok := githubAccounts[serverName]
		if !ok {
			return "", fmt.Errorf("no renewable GitHub credential for %q", serverName)
		}
		return githubapp.TokenForMCPAccount(refreshCtx, e.q, account, taskProject)
	})
	callTool, discoverTool := tools.NewMCPTools(store)
	registry.Register(callTool)
	registry.Register(discoverTool)

	closeIntegrations := func() {}
	if servers, err := e.q.ListCodegraphProjectServers(ctx, task.CompanyID); err == nil && len(servers) > 0 {
		if !allCompanyMCP {
			if assignments, assignErr := e.q.GetAgentCodegraphAssignments(ctx, agent.ID); assignErr == nil && len(assignments) > 0 {
				filtered := make([]db.CodegraphProjectServer, 0, len(servers))
				for _, server := range servers {
					if enabled, explicit := assignments[server.Server.ID]; !explicit || enabled {
						filtered = append(filtered, server)
					}
				}
				servers = filtered
			}
		}
		if len(servers) > 0 {
			proxy := tools.NewCodegraphProxy(taskProject, servers)
			summary := proxy.RegisterAll(ctx, registry)
			closeIntegrations = proxy.Close
			e.logInfo(logger, fmt.Sprintf("Codegraph: %d project(s) available", len(servers)))
			e.logInfo(logger, summary)
		}
	} else if err != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to load codegraph servers: %v", err))
	}

	if !allCompanyMCP && strings.TrimSpace(agent.Permissions) != "" {
		var permissions map[string]string
		if err := json.Unmarshal([]byte(agent.Permissions), &permissions); err != nil {
			e.logInfo(logger, fmt.Sprintf("Warning: invalid agent tool permissions: %v; leaving tools unchanged", err))
		} else {
			var denied []string
			for name, value := range permissions {
				if strings.EqualFold(strings.TrimSpace(value), "deny") {
					denied = append(denied, name)
				}
			}
			registry = registry.Exclude(denied)
		}
	}
	systemPrompt += registry.PromptListing()

	var listingCostTotal int
	var listingCostByServer map[string]int
	var accounts []db.MCPAccount
	var allServers []db.MCPServer
	var accountErr error
	if allCompanyMCP {
		allServers, accountErr = e.q.ListMCPServers(ctx, task.CompanyID, e.ownerUserIDForCompany(ctx, task.CompanyID))
		for _, server := range allServers {
			accounts = append(accounts, server.Accounts...)
		}
	} else {
		accounts, accountErr = e.q.ListMCPAccountsForAgent(ctx, agent.ID)
		allServers, _ = e.q.ListMCPServers(ctx, 0, 0)
	}
	if accountErr == nil && len(accounts) > 0 {
		serverByID := make(map[int32]db.MCPServer, len(allServers))
		for _, server := range allServers {
			serverByID[server.ID] = server
		}
		toolFilters, _ := e.q.GetAgentMCPToolFilters(ctx, agent.ID)
		allowedMCPs := decodeAgentNames(agent.AllowedMCPs)
		for _, account := range accounts {
			server, ok := serverByID[account.MCPServerID]
			if !ok || server.Transport == "builtin" || (!allCompanyMCP && !mcpAllowed(server.Name, allowedMCPs)) {
				continue
			}
			synthetic := server
			synthetic.Name = fmt.Sprintf("%s/%s", server.Name, account.Name)
			if server.IsGitHub() {
				token, tokenErr := githubapp.TokenForMCPAccount(ctx, e.q, account, taskProject)
				if tokenErr != nil || token == "" {
					if tokenErr == nil {
						tokenErr = fmt.Errorf("empty installation token")
					}
					e.logInfo(logger, fmt.Sprintf("Warning: skipping GitHub MCP account %q: installation token failed: %v", account.Name, tokenErr))
					continue
				}
				synthetic.AuthToken = token
				githubAccounts[synthetic.Name] = account
			} else {
				token, decryptErr := secrets.Default().Decrypt(account.AuthTokenEncrypted)
				if decryptErr != nil {
					e.logInfo(logger, fmt.Sprintf("Warning: skipping MCP account %q: %v", account.Name, decryptErr))
					continue
				}
				synthetic.AuthToken = token
			}
			store.AddExternalServer(synthetic)
			accountIDByName[synthetic.Name] = account.ID
			serverIDByName[synthetic.Name] = synthetic.ID
			if filters, ok := toolFilters[server.ID]; ok {
				disabled := make(map[string]bool)
				for toolName, enabled := range filters {
					if !enabled {
						disabled[toolName] = true
					}
				}
				if len(disabled) > 0 {
					store.SetDisabledTools(synthetic.Name, disabled)
				}
			}
		}
		if names := store.ServerNames(); len(names) > 0 {
			e.logInfo(logger, "MCP: "+strings.Join(names, ", "))
			systemPrompt += store.CompactListing()
			listingCostByServer = store.ListingCostByServer()
			for _, cost := range listingCostByServer {
				listingCostTotal += cost
			}
		}
	} else if accountErr != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to load MCP accounts: %v", accountErr))
	}
	e.logInfo(logger, "Effective tools: "+strings.Join(registry.Names(), ", "))
	return sessionIntegrations{registry, systemPrompt, listingCostTotal, listingCostByServer, closeIntegrations}
}

func mcpAllowed(name string, allowed []string) bool {
	if allowed == nil {
		return true
	}
	for _, candidate := range allowed {
		if candidate == name {
			return true
		}
	}
	return false
}
