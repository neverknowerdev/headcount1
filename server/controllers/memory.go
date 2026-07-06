package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/hindsight"

	"github.com/go-chi/chi/v5"
)

// memoryService is the Hindsight memory layer, wired in from main. Package
// level because API instances are constructed ad hoc throughout the server.
var memoryService *hindsight.Service
var memoryManager *hindsight.Manager

func SetMemoryService(s *hindsight.Service, m *hindsight.Manager) {
	memoryService = s
	memoryManager = m
}

// MemoryService exposes the wired service to the rest of the server package.
func MemoryService() *hindsight.Service { return memoryService }

func (api *API) memoryClientOr503(w http.ResponseWriter) *hindsight.Client {
	if memoryManager == nil || memoryManager.Client() == nil {
		api.respondError(w, http.StatusServiceUnavailable, "memory backend is not available")
		return nil
	}
	return memoryManager.Client()
}

func (api *API) respondRawJSON(w http.ResponseWriter, data []byte, err error) {
	if err != nil {
		api.respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// GetMemoryStatus reports whether the memory backend is up.
func (api *API) GetMemoryStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{"available": false}
	if memoryManager != nil && memoryManager.Client() != nil {
		status["available"] = true
		status["url"] = memoryManager.BaseURL()
	}
	api.respondJSON(w, http.StatusOK, status)
}

// memoryBank describes one bank with a human-friendly label.
type memoryBank struct {
	BankID string `json:"bank_id"`
	Kind   string `json:"kind"` // "company"
	Label  string `json:"label"`
}

// ListMemoryBanks lists app-owned banks for a company. Project docs and
// agent run experience share one bank per company (see pkg/hindsight
// Service doc comment for why), so this always returns exactly one entry.
func (api *API) ListMemoryBanks(w http.ResponseWriter, r *http.Request) {
	if api.memoryClientOr503(w) == nil {
		return
	}
	compID, err := strconv.Atoi(r.URL.Query().Get("company_id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	var comp db.Company
	if err := api.db.First(&comp, compID).Error; err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}
	banks := []memoryBank{{
		BankID: hindsight.BankID(comp.ID),
		Kind:   "company",
		Label:  fmt.Sprintf("Memory — %s", comp.Name),
	}}
	api.respondJSON(w, http.StatusOK, banks)
}

func (api *API) GetMemoryGraph(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	bank := chi.URLParam(r, "bankID")
	params := url.Values{}
	for _, k := range []string{"limit", "q", "type", "tags", "document_id"} {
		if v := r.URL.Query().Get(k); v != "" {
			params.Set(k, v)
		}
	}
	data, err := c.GraphRaw(r.Context(), bank, params)
	api.respondRawJSON(w, data, err)
}

func (api *API) GetMemoryEntitiesGraph(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	params := url.Values{"limit": {"500"}}
	if v := r.URL.Query().Get("limit"); v != "" {
		params.Set("limit", v)
	}
	data, err := c.EntitiesGraphRaw(r.Context(), chi.URLParam(r, "bankID"), params)
	api.respondRawJSON(w, data, err)
}

func (api *API) ListMemoryUnits(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	params := url.Values{}
	for _, k := range []string{"q", "type", "document_id", "limit", "offset", "state"} {
		if v := r.URL.Query().Get(k); v != "" {
			params.Set(k, v)
		}
	}
	data, err := c.ListMemoriesRaw(r.Context(), chi.URLParam(r, "bankID"), params)
	api.respondRawJSON(w, data, err)
}

func (api *API) GetMemoryUnit(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	data, err := c.GetMemoryRaw(r.Context(), chi.URLParam(r, "bankID"), chi.URLParam(r, "memoryID"))
	api.respondRawJSON(w, data, err)
}

// UpdateMemoryUnit edits a memory's text/context. Body: {"text": "...", "context": "..."}
func (api *API) UpdateMemoryUnit(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	var patch map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	data, err := c.UpdateMemory(r.Context(), chi.URLParam(r, "bankID"), chi.URLParam(r, "memoryID"), patch)
	api.respondRawJSON(w, data, err)
}

// DeleteMemoryUnit soft-deletes a memory (Hindsight curation: invalidate —
// excluded from recall/consolidation but kept for audit and reversible).
func (api *API) DeleteMemoryUnit(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	data, err := c.UpdateMemory(r.Context(), chi.URLParam(r, "bankID"), chi.URLParam(r, "memoryID"),
		map[string]interface{}{"state": "invalidated", "reason": "deleted from Memory UI"})
	api.respondRawJSON(w, data, err)
}

// RecallMemory queries one bank. Body: {"query": "...", "budget": "mid"}
func (api *API) RecallMemory(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	var req struct {
		Query  string `json:"query"`
		Budget string `json:"budget"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Query) == "" {
		api.respondError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.Budget == "" {
		req.Budget = "mid"
	}
	resp, err := c.Recall(r.Context(), chi.URLParam(r, "bankID"), hindsight.RecallRequest{
		Query: req.Query, Budget: req.Budget, MaxTokens: 4096,
	})
	if err != nil {
		api.respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, resp)
}

// AskMemory runs a reflect (LLM-reasoned answer) against one bank.
// Body: {"query": "..."}
func (api *API) AskMemory(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Query) == "" {
		api.respondError(w, http.StatusBadRequest, "query is required")
		return
	}
	resp, err := c.Reflect(r.Context(), chi.URLParam(r, "bankID"), req.Query, "low")
	if err != nil {
		api.respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, resp)
}

func (api *API) GetMemoryStats(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	data, err := c.StatsRaw(r.Context(), chi.URLParam(r, "bankID"))
	api.respondRawJSON(w, data, err)
}

// GetMemoryBankConfig exposes the bank's resolved config (mission,
// disposition, directives applied by Service.EnsureBank) for display/debug
// in the Memory UI.
func (api *API) GetMemoryBankConfig(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	data, err := c.GetBankConfigRaw(r.Context(), chi.URLParam(r, "bankID"))
	api.respondRawJSON(w, data, err)
}

// ListMemoryDirectives exposes the bank's active directives for display/debug.
func (api *API) ListMemoryDirectives(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	data, err := c.ListDirectivesRaw(r.Context(), chi.URLParam(r, "bankID"))
	api.respondRawJSON(w, data, err)
}

// ListMentalModels lists the bank's mental models (name, source query,
// last-refreshed time) for the Memory UI's Insights tab.
func (api *API) ListMentalModels(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	data, err := c.ListMentalModelsRaw(r.Context(), chi.URLParam(r, "bankID"))
	api.respondRawJSON(w, data, err)
}

func (api *API) GetMentalModel(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	data, err := c.GetMentalModelRaw(r.Context(), chi.URLParam(r, "bankID"), chi.URLParam(r, "modelID"))
	api.respondRawJSON(w, data, err)
}

func (api *API) RefreshMentalModel(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	if err := c.RefreshMentalModel(r.Context(), chi.URLParam(r, "bankID"), chi.URLParam(r, "modelID")); err != nil {
		api.respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (api *API) DeleteMentalModel(w http.ResponseWriter, r *http.Request) {
	c := api.memoryClientOr503(w)
	if c == nil {
		return
	}
	if err := c.DeleteMentalModel(r.Context(), chi.URLParam(r, "bankID"), chi.URLParam(r, "modelID")); err != nil {
		api.respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (api *API) projectRepoPath(basePath string, comp db.Company, proj db.Project) string {
	return filesystem.NewManager(basePath).GetProjectRepoPath(comp, proj)
}

// StartProjectMemoryInit feeds a project's doc files into its memory bank in
// the background. Waits (bounded) for the memory backend to come up, since
// project creation can race the hindsight-api boot.
func (api *API) StartProjectMemoryInit(projectID int32) {
	if memoryService == nil {
		return
	}
	go func() {
		ctx := context.Background()
		for i := 0; i < 60 && !memoryService.Available(); i++ {
			time.Sleep(10 * time.Second)
		}
		if !memoryService.Available() {
			log.Printf("memory: backend never came up, skipping doc ingestion for project %d", projectID)
			return
		}
		project, err := api.q.GetProject(ctx, projectID)
		if err != nil {
			return
		}
		var comp db.Company
		if err := api.db.First(&comp, project.CompanyID).Error; err != nil {
			return
		}
		settings := LoadSettings()
		repoPath := api.projectRepoPath(settings.BasePath, comp, project)
		if _, statErr := os.Stat(repoPath); statErr != nil {
			return // no repo on disk — nothing to feed
		}
		added, updated, removed, serr := memoryService.SyncProjectDocs(ctx, comp, project, repoPath)
		if serr != nil {
			log.Printf("memory: doc sync failed for project %d: %v", projectID, serr)
			return
		}
		log.Printf("memory: project %d docs synced (added=%d updated=%d removed=%d)", projectID, added, updated, removed)
	}()
}

// SyncAllProjectMemory re-syncs doc files of every project. Called on startup
// and after a filesystem sync, so edited/added/removed .md files are tracked.
func (api *API) SyncAllProjectMemory(ctx context.Context) {
	if memoryService == nil {
		return
	}
	var projects []db.Project
	if err := api.db.WithContext(ctx).Find(&projects).Error; err != nil {
		return
	}
	for _, p := range projects {
		api.StartProjectMemoryInit(p.ID)
	}
}

// SyncProjectMemory (re)ingests a project's .md files into its memory bank.
func (api *API) SyncProjectMemory(w http.ResponseWriter, r *http.Request) {
	if memoryService == nil || !memoryService.Available() {
		api.respondError(w, http.StatusServiceUnavailable, "memory backend is not available")
		return
	}
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	project, err := api.q.GetProject(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "project not found")
		return
	}
	var comp db.Company
	if err := api.db.First(&comp, project.CompanyID).Error; err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}
	settings := LoadSettings()
	repoPath := api.projectRepoPath(settings.BasePath, comp, project)
	added, updated, removed, serr := memoryService.SyncProjectDocs(r.Context(), comp, project, repoPath)
	if serr != nil {
		api.respondError(w, http.StatusInternalServerError, serr.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]int{"added": added, "updated": updated, "removed": removed})
}
