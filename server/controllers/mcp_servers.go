package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/mcp"
	"agent-orchestrator/pkg/filesystem"
	"github.com/go-chi/chi/v5"
)

// categorizeMCPError returns a human-readable reason for a discovery failure.
func categorizeMCPError(serverName, raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "executable file not found") || strings.Contains(lower, "no such file or directory"):
		if serverName == "github" {
			return "Binary not installed. Run: brew install github-mcp-server"
		}
		return "Command not found: " + raw
	case strings.Contains(lower, "eof"):
		if serverName == "google-docs" {
			return "Credentials file not found or invalid. Make sure the path you entered points to a valid Google service account JSON file."
		}
		if serverName == "github" {
			return "GitHub MCP server exited unexpectedly. Your token may be missing or invalid — try Re-authenticate."
		}
		return "Server exited unexpectedly. Check your configuration."
	case strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "authentication failed") || strings.Contains(lower, "bad credentials") ||
		strings.Contains(lower, "auth check"):
		return "Auth token invalid or expired. Re-authenticate."
	case strings.Contains(lower, "permission denied"):
		return "Permission denied. Check your auth token has the required scopes."
	default:
		return raw
	}
}

// discoverServerTools connects to a single server, lists tools, returns JSON or an error.
func discoverServerTools(ctx context.Context, s db.MCPServer) (string, error) {
	client, err := mcp.NewClient(s)
	if err != nil {
		return "", err
	}
	defer client.Close()

	discCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if _, err := client.Initialize(discCtx); err != nil {
		return "", err
	}
	tools, err := client.ListTools(discCtx)
	if err != nil {
		return "", err
	}

	// Auth probe: some servers (e.g. GitHub) serve tools/list without credentials
	// but fail on actual tool calls. Try a known read-only canary to catch this early.
	authCanaries := []string{"get_me", "whoami", "me", "current_user"}
	for _, canary := range authCanaries {
		for _, t := range tools {
			if t.Name == canary {
				if _, callErr := client.CallTool(discCtx, canary, json.RawMessage("{}")); callErr != nil {
					lower := strings.ToLower(callErr.Error())
					if strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized") ||
						strings.Contains(lower, "bad credentials") || strings.Contains(lower, "authentication") ||
						strings.Contains(lower, "forbidden") {
						return "", fmt.Errorf("auth check (%s): %w", canary, callErr)
					}
				}
				break
			}
		}
	}

	b, err := json.Marshal(tools)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// discoverServerToolsWithAccount connects using a specific account's credentials.
func discoverServerToolsWithAccount(ctx context.Context, s db.MCPServer, account db.MCPAccount) (string, error) {
	s.AuthToken = account.AuthToken
	return discoverServerTools(ctx, s)
}

// DiscoverAndCacheAllMCPTools discovers and caches tools for every enabled MCP
// server via its accounts. Called in a background goroutine on startup.
func (api *API) DiscoverAndCacheAllMCPTools(ctx context.Context) {
	servers, err := api.q.ListMCPServers(ctx, 0) // 0 = all companies
	if err != nil {
		log.Printf("MCP cache: failed to list servers: %v", err)
		return
	}

	for _, s := range servers {
		// For external servers, try each account until one succeeds.
		if len(s.Accounts) == 0 {
			continue
		}
		serverCached := false
		for _, acc := range s.Accounts {
			toolsJSON, discErr := discoverServerToolsWithAccount(ctx, s, acc)
			if discErr != nil {
				msg := categorizeMCPError(s.Name, discErr.Error())
				log.Printf("MCP cache: %s/%s: %s", s.Name, acc.Name, msg)
				_ = api.q.UpdateMCPAccountLastError(ctx, acc.ID, msg)
			} else {
				_ = api.q.UpdateMCPAccountLastError(ctx, acc.ID, "")
				if !serverCached {
					sorted := api.sortToolsByPopularity(ctx, s.ID, toolsJSON)
					_ = api.q.UpdateMCPServerToolsCache(ctx, s.ID, sorted)
					log.Printf("MCP cache: %s/%s: tools cached", s.Name, acc.Name)
					serverCached = true
				}
			}
		}
		if !serverCached {
			// All accounts failed — clear the cache so the UI shows "Setup required".
			_ = api.q.UpdateMCPServerLastError(ctx, s.ID, "All accounts failed to connect.")
		} else {
			log.Printf("MCP cache: %s: tools cached", s.Name)
		}
	}
}

// sortToolsByPopularity re-orders a tools JSON array by descending call count,
// using the mcp_tool_stats table. Falls back to the original order on any error.
func (api *API) sortToolsByPopularity(ctx context.Context, serverID int32, toolsJSON string) string {
	counts, err := api.q.GetMCPToolCallCounts(ctx, serverID)
	if err != nil || len(counts) == 0 {
		return toolsJSON
	}
	var mcpTools []mcp.Tool
	if json.Unmarshal([]byte(toolsJSON), &mcpTools) != nil {
		return toolsJSON
	}
	sort.SliceStable(mcpTools, func(i, j int) bool {
		ci, cj := counts[mcpTools[i].Name], counts[mcpTools[j].Name]
		if ci != cj {
			return ci > cj
		}
		return mcpTools[i].Name < mcpTools[j].Name
	})
	b, err := json.Marshal(mcpTools)
	if err != nil {
		return toolsJSON
	}
	return string(b)
}

func (api *API) ListMCPServers(w http.ResponseWriter, r *http.Request) {
	companyID, _ := strconv.Atoi(r.URL.Query().Get("company_id"))
	servers, err := api.q.ListMCPServers(r.Context(), int32(companyID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, servers)
}

func (api *API) CreateMCPServer(w http.ResponseWriter, r *http.Request) {
	var req db.MCPServer
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if req.Name == "" || req.Transport == "" {
		api.respondError(w, http.StatusBadRequest, "name and transport are required")
		return
	}
	req.Builtin = false // UI-created servers are never builtin
	s, err := api.q.CreateMCPServer(r.Context(), req)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.saveMCPServerToDisk(s)
	api.respondJSON(w, http.StatusCreated, s)
}

func (api *API) GetMCPServer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	s, err := api.q.GetMCPServer(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	api.respondJSON(w, http.StatusOK, s)
}

func (api *API) UpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := api.q.GetMCPServer(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}

	var req db.MCPServer
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	req.ID = existing.ID
	req.Builtin = existing.Builtin
	// Don't clear an existing auth token if the request sends an empty one.
	if req.AuthToken == "" {
		req.AuthToken = existing.AuthToken
	}
	// Predefined (builtin) servers have immutable transport config.
	if existing.Builtin {
		req.Name = existing.Name
		req.Transport = existing.Transport
		req.Command = existing.Command
		req.Args = existing.Args
		req.AuthType = existing.AuthType
		req.AuthEnvVar = existing.AuthEnvVar
	}
	// Preserve cached data
	req.ToolsCache = existing.ToolsCache
	req.LastError = existing.LastError

	s, err := api.q.UpdateMCPServer(r.Context(), req)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.saveMCPServerToDisk(s)
	api.respondJSON(w, http.StatusOK, s)
}

func (api *API) DeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	s, err := api.q.GetMCPServer(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	if s.Builtin {
		api.respondError(w, http.StatusForbidden, "built-in MCP servers cannot be deleted")
		return
	}
	if err := api.q.DeleteMCPServer(r.Context(), int32(id)); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.deleteMCPServerFromDisk(s.ID)
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DiscoverMCPServerTools tries each account in turn and caches tools on the server.
func (api *API) DiscoverMCPServerTools(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	s, err := api.q.GetMCPServer(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	if len(s.Accounts) == 0 {
		api.respondError(w, http.StatusBadRequest, "no accounts configured — add an account first")
		return
	}
	// Try the first account.
	acc := s.Accounts[0]
	toolsJSON, discErr := discoverServerToolsWithAccount(r.Context(), s, acc)
	if discErr != nil {
		msg := categorizeMCPError(s.Name, discErr.Error())
		_ = api.q.UpdateMCPAccountLastError(r.Context(), acc.ID, msg)
		api.respondError(w, http.StatusBadGateway, msg)
		return
	}
	_ = api.q.UpdateMCPAccountLastError(r.Context(), acc.ID, "")
	_ = api.q.UpdateMCPServerToolsCache(r.Context(), int32(id), toolsJSON)

	var tools []map[string]any
	_ = json.Unmarshal([]byte(toolsJSON), &tools)
	api.respondJSON(w, http.StatusOK, map[string]any{"tools": tools})
}

// ── MCPAccount handlers ───────────────────────────────────────────────────────

// mcpAccountInput is a DTO for create/update — AuthToken must be readable from JSON
// even though db.MCPAccount hides it with json:"-".
type mcpAccountInput struct {
	Name            string `json:"name"`
	AuthToken       string `json:"auth_token"`
	CredentialsJSON string `json:"credentials_json"` // base64-encoded JSON file content for credentials-file auth
}

// saveCredentialsFile writes JSON content to ~/.paperclip2/credentials/{name}.json
// and returns the file path to store as the auth token.
func saveCredentialsFile(name, jsonContent string) (string, error) {
	dir := filepath.Join(db.PaperclipHome(), "credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
	path := filepath.Join(dir, safe+"-credentials.json")
	if err := os.WriteFile(path, []byte(jsonContent), 0600); err != nil {
		return "", err
	}
	return path, nil
}

// CreateMCPAccount adds a new account (credentials) for an MCP server.
func (api *API) CreateMCPAccount(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid server id")
		return
	}
	var input mcpAccountInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if input.Name == "" {
		input.Name = "Default"
	}
	authToken := input.AuthToken
	if input.CredentialsJSON != "" {
		path, err := saveCredentialsFile(input.Name, input.CredentialsJSON)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, "failed to save credentials: "+err.Error())
			return
		}
		authToken = path
	}
	acc, err := api.q.CreateMCPAccount(r.Context(), db.MCPAccount{
		MCPServerID: int32(serverID),
		Name:        input.Name,
		AuthToken:   authToken,
	})
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusCreated, acc)
}

// UpdateMCPAccount updates name or auth_token for an account.
func (api *API) UpdateMCPAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "accountID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid account id")
		return
	}
	existing, err := api.q.GetMCPAccount(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	var input mcpAccountInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	existing.Name = input.Name
	if input.CredentialsJSON != "" {
		path, err := saveCredentialsFile(input.Name, input.CredentialsJSON)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, "failed to save credentials: "+err.Error())
			return
		}
		existing.AuthToken = path
		existing.LastError = ""
	} else if input.AuthToken != "" {
		existing.AuthToken = input.AuthToken
		existing.LastError = ""
	}
	acc, err := api.q.UpdateMCPAccount(r.Context(), existing)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, acc)
}

// DeleteMCPAccount removes an account and its agent assignments.
func (api *API) DeleteMCPAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "accountID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid account id")
		return
	}
	if err := api.q.DeleteMCPAccount(r.Context(), int32(id)); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DiscoverMCPAccountTools tests connectivity for a specific account.
func (api *API) DiscoverMCPAccountTools(w http.ResponseWriter, r *http.Request) {
	accountID, err := strconv.Atoi(chi.URLParam(r, "accountID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid account id")
		return
	}
	acc, err := api.q.GetMCPAccount(r.Context(), int32(accountID))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "account not found")
		return
	}
	s, err := api.q.GetMCPServer(r.Context(), acc.MCPServerID)
	if err != nil {
		api.respondError(w, http.StatusNotFound, "server not found")
		return
	}
	toolsJSON, discErr := discoverServerToolsWithAccount(r.Context(), s, acc)
	if discErr != nil {
		msg := categorizeMCPError(s.Name, discErr.Error())
		_ = api.q.UpdateMCPAccountLastError(r.Context(), int32(accountID), msg)
		api.respondError(w, http.StatusBadGateway, msg)
		return
	}
	_ = api.q.UpdateMCPAccountLastError(r.Context(), int32(accountID), "")
	_ = api.q.UpdateMCPServerToolsCache(r.Context(), s.ID, toolsJSON)

	var tools []map[string]any
	_ = json.Unmarshal([]byte(toolsJSON), &tools)
	api.respondJSON(w, http.StatusOK, map[string]any{"tools": tools})
}

// ── Google OAuth2 flow ───────────────────────────────────────────────────────

// sanitizeName returns a filesystem-safe version of name.
func sanitizeName(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
}

// StartGoogleOAuth saves OAuth client credentials and launches the browser auth flow.
// The npx process opens the user's default browser; this endpoint returns immediately.
func (api *API) StartGoogleOAuth(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid server id")
		return
	}
	var input struct {
		AccountName   string `json:"account_name"`
		OAuthKeysJSON string `json:"oauth_keys_json"`
		AccountID     int32  `json:"account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if input.AccountName == "" {
		input.AccountName = "Default"
	}
	if input.OAuthKeysJSON == "" {
		api.respondError(w, http.StatusBadRequest, "oauth_keys_json is required")
		return
	}

	keysPath, err := saveCredentialsFile(input.AccountName+"-oauth-keys", input.OAuthKeysJSON)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to save OAuth keys: "+err.Error())
		return
	}

	dir := filepath.Join(db.PaperclipHome(), "credentials")
	tokenPath := filepath.Join(dir, sanitizeName(input.AccountName)+"-gdrive-token.json")

	// Remove stale token so polling can detect the new auth.
	os.Remove(tokenPath)

	go func() {
		cmd := exec.Command("npx", "-y", "@modelcontextprotocol/server-gdrive", "auth")
		cmd.Env = append(os.Environ(),
			"GDRIVE_OAUTH_PATH="+keysPath,
			"GDRIVE_CREDENTIALS_PATH="+tokenPath,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("Google OAuth flow error (server %d): %v\n%s", serverID, err, out)
		}
	}()

	api.respondJSON(w, http.StatusOK, map[string]any{
		"status":       "waiting",
		"token_path":   tokenPath,
		"account_name": input.AccountName,
		"account_id":   input.AccountID,
	})
}

// PollGoogleOAuth checks whether the OAuth token file has been written and, on first
// success, creates (or updates) the MCPAccount with the token path as auth_token.
func (api *API) PollGoogleOAuth(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid server id")
		return
	}
	tokenPath := r.URL.Query().Get("token_path")
	accountName := r.URL.Query().Get("account_name")
	accountIDStr := r.URL.Query().Get("account_id")

	if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
		api.respondJSON(w, http.StatusOK, map[string]any{"ready": false})
		return
	}

	accountID, _ := strconv.Atoi(accountIDStr)

	var acc db.MCPAccount
	if accountID > 0 {
		existing, err := api.q.GetMCPAccount(r.Context(), int32(accountID))
		if err != nil {
			api.respondError(w, http.StatusNotFound, "account not found")
			return
		}
		existing.AuthToken = tokenPath
		existing.LastError = ""
		acc, err = api.q.UpdateMCPAccount(r.Context(), existing)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		acc, err = api.q.CreateMCPAccount(r.Context(), db.MCPAccount{
			MCPServerID: int32(serverID),
			Name:        accountName,
			AuthToken:   tokenPath,
		})
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	api.respondJSON(w, http.StatusOK, map[string]any{"ready": true, "account": acc})
}

// ── Agent-account assignment handlers ────────────────────────────────────────

// GetAgentMCPAccounts returns account assignments and codegraph server assignments for an agent.
func (api *API) GetAgentMCPAccounts(w http.ResponseWriter, r *http.Request) {
	agentID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	accounts, err := api.q.ListAllAgentMCPAccountAssignments(r.Context(), int32(agentID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cgMap, _ := api.q.GetAgentCodegraphAssignments(r.Context(), int32(agentID))
	type cgRow struct {
		ServerID int32 `json:"server_id"`
		Enabled  bool  `json:"enabled"`
	}
	cgRows := make([]cgRow, 0, len(cgMap))
	for sid, en := range cgMap {
		cgRows = append(cgRows, cgRow{ServerID: sid, Enabled: en})
	}
	api.respondJSON(w, http.StatusOK, map[string]any{
		"accounts":  accounts,
		"codegraph": cgRows,
	})
}

// SetAgentMCPAccounts replaces account assignments and codegraph server assignments for an agent.
func (api *API) SetAgentMCPAccounts(w http.ResponseWriter, r *http.Request) {
	agentID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	var payload struct {
		Accounts  []db.AgentMCPAccount `json:"accounts"`
		Codegraph []struct {
			ServerID int32 `json:"server_id"`
			Enabled  bool  `json:"enabled"`
		} `json:"codegraph"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if err := api.q.SetAgentMCPAccounts(r.Context(), int32(agentID), payload.Accounts); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cgAssignments := make([]db.AgentMCPServer, 0, len(payload.Codegraph))
	for _, cg := range payload.Codegraph {
		cgAssignments = append(cgAssignments, db.AgentMCPServer{MCPServerID: cg.ServerID, Enabled: cg.Enabled})
	}
	if err := api.q.SetAgentCodegraphAssignments(r.Context(), int32(agentID), cgAssignments); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetAgentMCPServers returns all MCP server assignments for an agent.
func (api *API) GetAgentMCPServers(w http.ResponseWriter, r *http.Request) {
	agentID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	assignments, err := api.q.ListAllAgentMCPAssignments(r.Context(), int32(agentID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, assignments)
}

// SetAgentMCPServers replaces all MCP assignments for an agent.
func (api *API) SetAgentMCPServers(w http.ResponseWriter, r *http.Request) {
	agentID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	var assignments []db.AgentMCPServer
	if err := json.NewDecoder(r.Body).Decode(&assignments); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if err := api.q.SetAgentMCPServers(r.Context(), int32(agentID), assignments); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (api *API) saveMCPServerToDisk(s db.MCPServer) {
	settings := LoadSettings()
	fm := filesystem.NewManager(settings.BasePath)
	if err := fm.SaveMCPServer(s); err != nil {
		log.Printf("Warning: failed to write MCP server %d to disk: %v", s.ID, err)
	}
}

func (api *API) deleteMCPServerFromDisk(id int32) {
	settings := LoadSettings()
	fm := filesystem.NewManager(settings.BasePath)
	if err := fm.DeleteMCPServerFile(id); err != nil {
		log.Printf("Warning: failed to delete MCP server file %d: %v", id, err)
	}
}
