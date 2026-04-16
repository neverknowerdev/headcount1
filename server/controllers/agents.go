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
	agents, err := api.q.ListAgentsByCompany(r.Context(), int32(compID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, agents)
}

func (api *API) CreateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID    int32  `json:"company_id"`
		Name         string `json:"name"`
		Description  string `json:"description"`
		SystemPrompt string `json:"system_prompt"`
		Model        string `json:"model"`
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
	}

	agent, err := api.q.CreateAgent(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.logActivity(req.CompanyID, "agent_created", int32(agent.ID), "agent", "")

	api.respondJSON(w, http.StatusCreated, agent)
}
