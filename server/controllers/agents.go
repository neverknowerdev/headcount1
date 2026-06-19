package endpoints

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
	"github.com/go-chi/chi/v5"
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

	// Write agent metadata to filesystem
	settings := LoadSettings()
	storage := filesystem.NewStorage(settings.BasePath)
	var comp db.Company
	api.db.First(&comp, req.CompanyID)
	if err := storage.WriteAgent(agent, comp.ShortName); err != nil {
		log.Printf("Warning: failed to write agent metadata: %v", err)
	}

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
	out := make([]RunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunResponse(run))
	}
	api.respondJSON(w, http.StatusOK, out)
}
