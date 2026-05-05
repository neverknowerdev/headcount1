package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"agent-orchestrator/db"
	"github.com/go-chi/chi/v5"
)

func saveOpenCodeAgentConfig(agent db.Agent, providerType string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	agentsDir := filepath.Join(homeDir, ".config", "opencode", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("failed to create agents directory: %w", err)
	}

	agentPath := filepath.Join(agentsDir, fmt.Sprintf("%s.md", agent.Name))

	permissions := agent.Permissions
	if permissions == "" {
		permissions = "{}"
	}

	var perms map[string]string
	if err := json.Unmarshal([]byte(permissions), &perms); err != nil {
		perms = make(map[string]string)
	}
	perms["update_task_status"] = "allow"

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("description: %s\n", agent.Description))
	mode := agent.Mode
	if mode == "" {
		mode = "primary"
	}
	sb.WriteString(fmt.Sprintf("mode: %s\n", mode))

	if agent.Model != "" {
		if providerType != "" && !strings.Contains(agent.Model, "/") {
			sb.WriteString(fmt.Sprintf("model: %s/%s\n", providerType, agent.Model))
		} else {
			sb.WriteString(fmt.Sprintf("model: %s\n", agent.Model))
		}
	}

	sb.WriteString("permission:\n")
	for k, v := range perms {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(agent.SystemPrompt)
	sb.WriteString("\n")

	return os.WriteFile(agentPath, []byte(sb.String()), 0644)
}

func deleteOpenCodeAgentConfig(agentName string) {
	homeDir, err := os.UserHomeDir()
	if err == nil {
		agentPath := filepath.Join(homeDir, ".config", "opencode", "agents", fmt.Sprintf("%s.md", agentName))
		os.Remove(agentPath)
	}
}

func (api *API) ListAgents(w http.ResponseWriter, r *http.Request) {
	compID, err := strconv.Atoi(r.URL.Query().Get("company_id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	agents, err := api.q.ListAgentsByCompany(r.Context(), int32(compID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, agents)
}

func (api *API) GetAgent(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	agent, err := api.q.GetAgent(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "agent not found")
		return
	}
	api.respondJSON(w, http.StatusOK, agent)
}

func (api *API) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		SystemPrompt string `json:"system_prompt"`
		Model        string `json:"model"`
		Mode         string `json:"mode"`
		Permissions  string `json:"permissions"`
		ProviderID   *int32 `json:"provider_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}

	agent, err := api.q.GetAgent(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "agent not found")
		return
	}

	oldName := agent.Name

	if req.Name != "" {
		agent.Name = req.Name
	}
	agent.Description = req.Description
	if req.SystemPrompt != "" {
		agent.SystemPrompt = req.SystemPrompt
	}
	if req.Model != "" {
		agent.Model = req.Model
	}
	if req.Mode != "" {
		agent.Mode = req.Mode
	}
	if req.Permissions != "" {
		agent.Permissions = req.Permissions
	}
	agent.ProviderID = req.ProviderID

	agent, err = api.q.UpdateAgent(r.Context(), agent)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if oldName != agent.Name {
		deleteOpenCodeAgentConfig(oldName)
	}

	providerType := ""
	if agent.ProviderID != nil {
		if p, err := api.q.GetLLMProvider(r.Context(), *agent.ProviderID); err == nil {
			providerType = p.ProviderType
			if providerType == "" {
				providerType = p.Name
			}
		}
	}
	_ = saveOpenCodeAgentConfig(agent, providerType)

	api.respondJSON(w, http.StatusOK, agent)
}

func (api *API) GetAgentStats(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var stats struct {
		TotalRequests    int `json:"total_requests"`
		TotalTokens      int `json:"total_tokens"`
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	}

	api.db.Model(&db.ProxyRequestLog{}).
		Where("agent_id = ?", id).
		Select("count(*) as total_requests, sum(total_tokens) as total_tokens, sum(prompt_tokens) as prompt_tokens, sum(completion_tokens) as completion_tokens").
		Scan(&stats)

	api.respondJSON(w, http.StatusOK, stats)
}

func (api *API) CreateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID    int32  `json:"company_id"`
		Name         string `json:"name"`
		Description  string `json:"description"`
		SystemPrompt string `json:"system_prompt"`
		Model        string `json:"model"`
		Mode         string `json:"mode"`
		Permissions  string `json:"permissions"`
		ProviderID   *int32 `json:"provider_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	p := db.Agent{
		CompanyID:    req.CompanyID,
		Name:         req.Name,
		SystemPrompt: req.SystemPrompt,
		Description:  req.Description,
		Model:        req.Model,
		Mode:         req.Mode,
		Permissions:  req.Permissions,
		ProviderID:   req.ProviderID,
	}

	agent, err := api.q.CreateAgent(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	providerType := ""
	if agent.ProviderID != nil {
		if prov, err := api.q.GetLLMProvider(r.Context(), *agent.ProviderID); err == nil {
			providerType = prov.ProviderType
			if providerType == "" {
				providerType = prov.Name
			}
		}
	}
	_ = saveOpenCodeAgentConfig(agent, providerType)

	api.logActivity(req.CompanyID, "agent_created", int32(agent.ID), "agent", "")

	api.respondJSON(w, http.StatusCreated, agent)
}

func (api *API) ListAgentRuns(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var runs []db.Run
	if err := api.db.Where("agent_id = ?", id).Order("started_at desc").Find(&runs).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, runs)
}
