package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/aicli"
)

func defaultCanUseWorkers(roleKey, name string) bool {
	for _, cfg := range agentconfig.BuiltinConfigs() {
		if agentconfig.RoleMatches(roleKey, name, cfg.Name) {
			return cfg.CanUseWorkers
		}
	}
	return false
}

func normalizeAllowedMCPs(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil // empty means all enabled MCPs
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return "", fmt.Errorf("allowed_mcps must be a JSON array of server names")
	}
	seen := make(map[string]struct{}, len(names))
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", fmt.Errorf("allowed_mcps[%d] must not be empty", i)
		}
		if _, ok := seen[name]; ok {
			return "", fmt.Errorf("allowed_mcps contains duplicate server %q", name)
		}
		seen[name] = struct{}{}
		names[i] = name
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// authorizeAgentBindings verifies the provider and model group an agent is
// bound to belong to the caller. Providers/groups are per-user and keyed by
// sequential int32, and secrets.Decrypt routes by the owner embedded in the
// ciphertext — so without this check an agent could point at another tenant's
// provider_id and spend their API key. Nil bindings are fine (agent falls back
// to the owner's default models).
func (api *API) authorizeAgentBindings(r *http.Request, providerID, modelGroupID *int32) error {
	if providerID != nil {
		if _, err := api.authorizeProvider(r, *providerID); err != nil {
			return err
		}
	}
	if modelGroupID != nil {
		if _, err := api.authorizeModelGroup(r, *modelGroupID); err != nil {
			return err
		}
	}
	return nil
}

func (api *API) ListAgents(w http.ResponseWriter, r *http.Request) {
	compID, err := strconv.Atoi(r.URL.Query().Get("company_id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	if _, err := api.authorizeCompany(r, int32(compID)); err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}
	agents, err := api.q.ListAgentsByCompany(r.Context(), int32(compID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, agents)
}

// GetToolNames returns the canonical native tool names used by the custom
// agent permissions UI. Keeping the list server-owned prevents frontend and
// runtime tool names from drifting apart.
func (api *API) GetToolNames(w http.ResponseWriter, _ *http.Request) {
	api.respondJSON(w, http.StatusOK, aicli.ConfigurableToolNames())
}

func (api *API) GetAgent(w http.ResponseWriter, r *http.Request) {
	api.respondJSON(w, http.StatusOK, api.agentFromCtx(r)) // loaded + authorized by LoadAgent
}

func (api *API) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           string `json:"name"`
		RoleKey        string `json:"role_key"`
		ShortName      string `json:"short_name"`
		Description    string `json:"description"`
		SystemPrompt   string `json:"system_prompt"`
		Model          string `json:"model"`
		ChatType       string `json:"chat_type"`
		ReasoningLevel string `json:"reasoning_level"`
		AllowedMCPs    string `json:"allowed_mcps"`
		Permissions    string `json:"permissions"`
		CanUseWorkers  *bool  `json:"can_use_workers"`
		Enabled        *bool  `json:"enabled"`
		ProviderID     *int32 `json:"provider_id"`
		ModelGroupID   *int32 `json:"model_group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	allowedMCPs, err := normalizeAllowedMCPs(req.AllowedMCPs)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	agent := api.agentFromCtx(r) // loaded + authorized by LoadAgent
	if agent.Builtin {
		// Built-in rows are the durable instances of the checked-in catalog. Their
		// role, prompt, hierarchy, and tool policy are immutable; the company may
		// only turn the role on or off.
		if req.Enabled != nil {
			agent.Enabled = *req.Enabled
		}
		updated, err := api.q.UpdateAgent(r.Context(), agent)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		api.respondJSON(w, http.StatusOK, updated)
		return
	}

	if req.Name != "" {
		agent.Name = req.Name
	}
	if req.RoleKey != "" {
		agent.RoleKey = req.RoleKey
	}
	if req.ShortName != "" {
		agent.ShortName = req.ShortName
	}
	agent.Description = req.Description
	if req.SystemPrompt != "" {
		agent.SystemPrompt = req.SystemPrompt
	}
	if req.Model != "" {
		agent.Model = req.Model
	}
	if req.ChatType != "" {
		agent.ChatType = req.ChatType
	}
	if req.ReasoningLevel != "" {
		agent.ReasoningLevel = req.ReasoningLevel
	}
	agent.AllowedMCPs = allowedMCPs
	if req.Permissions != "" {
		agent.Permissions = req.Permissions
	}
	if req.Enabled != nil {
		agent.Enabled = *req.Enabled
	}
	if req.CanUseWorkers != nil {
		agent.CanUseWorkers = *req.CanUseWorkers
	}
	if err := api.authorizeAgentBindings(r, req.ProviderID, req.ModelGroupID); err != nil {
		api.respondError(w, http.StatusNotFound, "provider or model group not found")
		return
	}
	agent.ProviderID = req.ProviderID
	agent.ModelGroupID = req.ModelGroupID

	updated, err := api.q.UpdateAgent(r.Context(), agent)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, updated)
}

func (api *API) GetAgentStats(w http.ResponseWriter, r *http.Request) {
	agent := api.agentFromCtx(r) // loaded + authorized by LoadAgent

	var stats struct {
		TotalRequests    int `json:"total_requests"`
		TotalTokens      int `json:"total_tokens"`
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	}

	api.db.Model(&db.ProxyRequestLog{}).
		Where("agent_id = ?", agent.ID).
		Select("count(*) as total_requests, sum(total_tokens) as total_tokens, sum(prompt_tokens) as prompt_tokens, sum(completion_tokens) as completion_tokens").
		Scan(&stats)

	api.respondJSON(w, http.StatusOK, stats)
}

func (api *API) CreateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID      int32  `json:"company_id"`
		Name           string `json:"name"`
		RoleKey        string `json:"role_key"`
		ShortName      string `json:"short_name"`
		Description    string `json:"description"`
		SystemPrompt   string `json:"system_prompt"`
		Model          string `json:"model"`
		ChatType       string `json:"chat_type"`
		ReasoningLevel string `json:"reasoning_level"`
		AllowedMCPs    string `json:"allowed_mcps"`
		Permissions    string `json:"permissions"`
		CanUseWorkers  *bool  `json:"can_use_workers"`
		Enabled        *bool  `json:"enabled"`
		ProviderID     *int32 `json:"provider_id"`
		ModelGroupID   *int32 `json:"model_group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	allowedMCPs, err := normalizeAllowedMCPs(req.AllowedMCPs)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := api.authorizeCompany(r, req.CompanyID); err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}
	if err := api.authorizeAgentBindings(r, req.ProviderID, req.ModelGroupID); err != nil {
		api.respondError(w, http.StatusNotFound, "provider or model group not found")
		return
	}
	p := db.Agent{
		CompanyID:      req.CompanyID,
		Name:           req.Name,
		RoleKey:        req.RoleKey,
		ShortName:      req.ShortName,
		SystemPrompt:   req.SystemPrompt,
		Description:    req.Description,
		Model:          req.Model,
		ChatType:       req.ChatType,
		ReasoningLevel: req.ReasoningLevel,
		AllowedMCPs:    allowedMCPs,
		Permissions:    req.Permissions,
		CanUseWorkers:  defaultCanUseWorkers(req.RoleKey, req.Name),
		ProviderID:     req.ProviderID,
		ModelGroupID:   req.ModelGroupID,
		Enabled:        req.Enabled == nil || *req.Enabled,
	}
	if req.CanUseWorkers != nil {
		p.CanUseWorkers = *req.CanUseWorkers
	}

	agent, err := api.q.CreateAgent(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.logActivity(req.CompanyID, "agent_created", int32(agent.ID), "agent", "")

	api.respondJSON(w, http.StatusCreated, agent)
}

// DeleteAgent removes a custom agent. Built-in agents are durable catalog
// instances and can only be disabled through UpdateAgent.
func (api *API) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	agent := api.agentFromCtx(r)
	if agent.Builtin {
		api.respondError(w, http.StatusForbidden, "built-in agents cannot be deleted — disable them instead")
		return
	}
	if err := api.q.DeleteAgent(r.Context(), agent.ID); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"message": "agent deleted"})
}

func (api *API) ListAgentRuns(w http.ResponseWriter, r *http.Request) {
	agent := api.agentFromCtx(r) // loaded + authorized by LoadAgent
	var runs []db.Run
	if err := api.db.Where("agent_id = ?", agent.ID).Order("started_at desc").Find(&runs).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]RunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunResponse(run))
	}
	api.respondJSON(w, http.StatusOK, out)
}
