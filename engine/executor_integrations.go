package engine

import (
	"context"
	"fmt"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/githubapp"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/secrets"
)

// sessionIntegrations is what connecting an executor to the company's
// integrations yields: its final tool registry and system prompt, and the
// prompt cost of listing the connected MCP servers.
type sessionIntegrations struct {
	registry            *aicli.Registry
	systemPrompt        string
	listingCostTotal    int
	listingCostByServer map[string]int
	close               func()
}

// configureSessionIntegrations gives an executor every integration its
// company has connected: all MCP servers and accounts, and the codegraph of
// each project. Executors are not restricted per agent; the listing of the
// tools actually registered is appended to the system prompt, so the prompt
// can never describe a tool that is not there.
func (e *NativeEngine) configureSessionIntegrations(ctx context.Context, task db.Task, registry *aicli.Registry, systemPrompt string, logger *logging.ProxyLogger) sessionIntegrations {
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
		proxy := tools.NewCodegraphProxy(taskProject, servers)
		summary := proxy.RegisterAll(ctx, registry)
		closeIntegrations = proxy.Close
		e.logInfo(logger, fmt.Sprintf("Codegraph: %d project(s) available", len(servers)))
		e.logInfo(logger, summary)
	} else if err != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to load codegraph servers: %v", err))
	}
	systemPrompt += registry.PromptListing()

	var listingCostTotal int
	var listingCostByServer map[string]int
	servers, err := e.q.ListMCPServers(ctx, task.CompanyID, e.ownerUserIDForCompany(ctx, task.CompanyID))
	if err != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to load MCP servers: %v", err))
	}
	for _, server := range servers {
		if server.Transport == "builtin" {
			continue
		}
		for _, account := range server.Accounts {
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
	e.logInfo(logger, "Tools: "+strings.Join(registry.Names(), ", "))
	return sessionIntegrations{registry, systemPrompt, listingCostTotal, listingCostByServer, closeIntegrations}
}
