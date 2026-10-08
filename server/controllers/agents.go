package endpoints

import (
	"encoding/json"
	"net/http"
	"strconv"

	"agent-orchestrator/db"
)

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

func (api *API) GetAgent(w http.ResponseWriter, r *http.Request) {
	api.respondJSON(w, http.StatusOK, api.agentFromCtx(r)) // loaded + authorized by LoadAgent
}

// UpdateAgent edits an agent's role. A built-in agent keeps its identity and
// stays enabled; its description and prompt are the company's to change.
func (api *API) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		RoleKey      string `json:"role_key"`
		ShortName    string `json:"short_name"`
		Description  string `json:"description"`
		SystemPrompt string `json:"system_prompt"`
		Enabled      *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}

	agent := api.agentFromCtx(r) // loaded + authorized by LoadAgent
	if agent.Builtin {
		if req.Description != "" {
			agent.Description = req.Description
		}
		agent.Enabled = true
	} else {
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
		if req.Enabled != nil {
			agent.Enabled = *req.Enabled
		}
	}
	if req.SystemPrompt != "" {
		agent.SystemPrompt = req.SystemPrompt
	}

	updated, err := api.q.UpdateAgent(r.Context(), agent)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, updated)
}

func (api *API) CreateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID    int32  `json:"company_id"`
		Name         string `json:"name"`
		RoleKey      string `json:"role_key"`
		ShortName    string `json:"short_name"`
		Description  string `json:"description"`
		SystemPrompt string `json:"system_prompt"`
		Enabled      *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	if _, err := api.authorizeCompany(r, req.CompanyID); err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}
	agent, err := api.q.CreateAgent(r.Context(), db.Agent{
		CompanyID:    req.CompanyID,
		Name:         req.Name,
		RoleKey:      req.RoleKey,
		ShortName:    req.ShortName,
		SystemPrompt: req.SystemPrompt,
		Description:  req.Description,
		Enabled:      req.Enabled == nil || *req.Enabled,
	})
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.logActivity(req.CompanyID, "agent_created", int32(agent.ID), "agent", "")

	api.respondJSON(w, http.StatusCreated, agent)
}

// DeleteAgent removes a custom agent. Built-in agents are durable catalog
// instances and cannot be deleted.
func (api *API) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	agent := api.agentFromCtx(r)
	if agent.Builtin {
		api.respondError(w, http.StatusForbidden, "built-in agents cannot be deleted")
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
	if err := runOverview(api.db).Where("agent_id = ?", agent.ID).Order("started_at desc").Find(&runs).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondRuns(w, r, runs)
}
