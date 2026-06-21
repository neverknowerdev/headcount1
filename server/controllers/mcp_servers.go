package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/mcp"
	"github.com/go-chi/chi/v5"
)

func (api *API) ListMCPServers(w http.ResponseWriter, r *http.Request) {
	companyIDStr := r.URL.Query().Get("company_id")
	companyID, err := strconv.Atoi(companyIDStr)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "company_id required")
		return
	}
	servers, err := api.q.ListMCPServersByCompany(r.Context(), int32(companyID))
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
	if req.Name == "" || req.Transport == "" || req.CompanyID == 0 {
		api.respondError(w, http.StatusBadRequest, "name, transport, and company_id are required")
		return
	}
	req.Builtin = false // UI-created servers are never builtin
	s, err := api.q.CreateMCPServer(r.Context(), req)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	// Preserve immutable fields.
	req.ID = existing.ID
	req.CompanyID = existing.CompanyID
	req.Builtin = existing.Builtin
	// Don't clear an existing auth token if the request sends an empty one.
	if req.AuthToken == "" {
		req.AuthToken = existing.AuthToken
	}

	s, err := api.q.UpdateMCPServer(r.Context(), req)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DiscoverMCPServerTools connects to the MCP server and returns its tool list.
// This is used by the UI to preview available tools.
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
	if s.Transport == "builtin" {
		tools := []map[string]string{
			{"name": "update_task_status", "description": "Update the status of the current task."},
			{"name": "create_subtask", "description": "Create a new subtask and assign it to a subagent."},
		}
		api.respondJSON(w, http.StatusOK, map[string]any{"tools": tools})
		return
	}

	client, err := mcp.NewClient(s)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if _, err := client.Initialize(ctx); err != nil {
		api.respondError(w, http.StatusBadGateway, "MCP initialize failed: "+err.Error())
		return
	}
	mcpTools, err := client.ListTools(ctx)
	if err != nil {
		api.respondError(w, http.StatusBadGateway, "MCP tools/list failed: "+err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]any{"tools": mcpTools})
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
