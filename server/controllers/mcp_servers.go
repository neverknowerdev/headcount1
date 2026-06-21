package endpoints

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/mcp"
	"agent-orchestrator/pkg/filesystem"
	"github.com/go-chi/chi/v5"
)

// DiscoverAndCacheAllMCPTools runs tool discovery for all enabled MCP servers
// and stores the results in tools_cache. Called in a background goroutine on startup.
func (api *API) DiscoverAndCacheAllMCPTools(ctx context.Context) {
	servers, err := api.q.ListMCPServers(ctx)
	if err != nil {
		log.Printf("MCP cache: failed to list servers: %v", err)
		return
	}
	for _, s := range servers {
		if !s.Enabled {
			continue
		}
		var toolsJSON string
		if s.Transport == "builtin" {
			// Paperclip2: hardcode tools
			type slimTool struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			builtins := []slimTool{
				{Name: "update_task_status", Description: "Update the status of the current task (to-do, in-progress, in-review, done, blocked, cancelled)."},
				{Name: "create_subtask", Description: "Create a new subtask and assign it to a sub-agent for execution."},
			}
			if b, err := json.Marshal(builtins); err == nil {
				toolsJSON = string(b)
			}
		} else {
			client, err := mcp.NewClient(s)
			if err != nil {
				log.Printf("MCP cache: %s: connect failed: %v", s.Name, err)
				continue
			}
			discCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, initErr := client.Initialize(discCtx)
			if initErr == nil {
				tools, listErr := client.ListTools(discCtx)
				if listErr == nil {
					if b, err := json.Marshal(tools); err == nil {
						toolsJSON = string(b)
					}
				} else {
					log.Printf("MCP cache: %s: list tools failed: %v", s.Name, listErr)
				}
			} else {
				log.Printf("MCP cache: %s: initialize failed: %v", s.Name, initErr)
			}
			client.Close()
			cancel()
		}
		if toolsJSON != "" {
			if err := api.q.UpdateMCPServerToolsCache(ctx, s.ID, toolsJSON); err != nil {
				log.Printf("MCP cache: %s: save cache failed: %v", s.Name, err)
			} else {
				log.Printf("MCP cache: %s: cached tools", s.Name)
			}
		}
	}
}

func (api *API) ListMCPServers(w http.ResponseWriter, r *http.Request) {
	servers, err := api.q.ListMCPServers(r.Context())
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

// DiscoverMCPServerTools connects to the MCP server and returns its tool list.
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
	// Persist discovered tools to cache so they survive restarts and load on page open.
	if b, err := json.Marshal(mcpTools); err == nil {
		_ = api.q.UpdateMCPServerToolsCache(r.Context(), int32(id), string(b))
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
