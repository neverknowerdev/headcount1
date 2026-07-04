package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/mempalace"
	"agent-orchestrator/pkg/setup"
)

// Memory section API — thin proxy over the company's mempalace MCP server
// (reads/corrections) and the mempalace CLI (maintenance). Every handler
// resolves the company from ?company_id, checks availability, and answers
// 503 with an explanatory payload while the memory layer is not usable.

// memoryCompany resolves the company and its palace server for a request.
// Writes the error response itself and returns ok=false when unavailable.
func (api *API) memoryCompany(w http.ResponseWriter, r *http.Request) (db.Company, db.MCPServer, bool) {
	companyID, _ := strconv.Atoi(r.URL.Query().Get("company_id"))
	if companyID == 0 {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return db.Company{}, db.MCPServer{}, false
	}
	company, err := api.q.GetCompany(r.Context(), int32(companyID))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return db.Company{}, db.MCPServer{}, false
	}
	if !mempalace.Available() {
		api.respondJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":     "memory layer unavailable — mempalace is not installed (or setup is still running)",
			"available": false,
		})
		return db.Company{}, db.MCPServer{}, false
	}
	// Lazy provisioning: companies created while setup was still running get
	// their palace on first touch.
	server, err := mempalace.ServerForCompany(r.Context(), api.q, company)
	if err != nil {
		server, err = mempalace.EnsureCompanyServer(r.Context(), api.q, company)
	}
	if err != nil || !mempalace.Ready(server) {
		api.respondJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":     "memory palace is not ready for this company yet",
			"available": true,
		})
		return db.Company{}, db.MCPServer{}, false
	}
	return company, server, true
}

// respondRawJSON writes a string that is already a JSON document; non-JSON
// output is wrapped so clients always get valid JSON.
func (api *API) respondRawJSON(w http.ResponseWriter, out string) {
	trimmed := strings.TrimSpace(out)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		api.respondJSON(w, http.StatusOK, map[string]string{"result": out})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(trimmed))
}

func (api *API) memoryCall(w http.ResponseWriter, r *http.Request, server db.MCPServer, tool string, args map[string]any) (string, bool) {
	out, err := mempalace.CallServerTool(r.Context(), server, tool, args)
	if err != nil {
		api.respondError(w, http.StatusBadGateway, "memory call failed: "+err.Error())
		return "", false
	}
	return out, true
}

func (api *API) logMemoryActivity(ctx context.Context, companyID int32, tool, kind, wing, room, query string, resultN int) {
	_, _ = api.q.CreateMemoryActivity(ctx, db.MemoryActivity{
		CompanyID: companyID, AgentName: "", Tool: tool, Kind: kind,
		Wing: wing, Room: room, Query: query, ResultN: resultN,
	})
}

// GetMemoryStatus reports availability + palace status. Unlike the other
// handlers it answers 200 even when memory is unavailable, so the UI can
// render install guidance instead of an error state.
func (api *API) GetMemoryStatus(w http.ResponseWriter, r *http.Request) {
	companyID, _ := strconv.Atoi(r.URL.Query().Get("company_id"))
	if companyID == 0 {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	company, err := api.q.GetCompany(r.Context(), int32(companyID))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}

	resp := map[string]any{"available": mempalace.Available(), "ready": false}
	if !mempalace.Available() {
		// Surface the setup soft-failure reason when there is one.
		for _, warn := range setup.Warnings() {
			if warn.Name == "mempalace" {
				resp["install_error"] = warn.Reason
			}
		}
		api.respondJSON(w, http.StatusOK, resp)
		return
	}

	server, err := mempalace.ServerForCompany(r.Context(), api.q, company)
	if err != nil {
		server, err = mempalace.EnsureCompanyServer(r.Context(), api.q, company)
	}
	if err != nil {
		resp["error"] = err.Error()
		api.respondJSON(w, http.StatusOK, resp)
		return
	}
	resp["ready"] = mempalace.Ready(server)
	resp["init_status"] = server.InitStatus

	if mempalace.Ready(server) {
		if out, sErr := mempalace.CallServerTool(r.Context(), server, "mempalace_status", map[string]any{}); sErr == nil {
			var status any
			if json.Unmarshal([]byte(out), &status) == nil {
				resp["palace"] = status
			}
		} else {
			resp["error"] = sErr.Error()
		}
	}
	api.respondJSON(w, http.StatusOK, resp)
}

func (api *API) GetMemoryTaxonomy(w http.ResponseWriter, r *http.Request) {
	_, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	if out, ok := api.memoryCall(w, r, server, "mempalace_get_taxonomy", map[string]any{}); ok {
		api.respondRawJSON(w, out)
	}
}

func (api *API) SearchMemory(w http.ResponseWriter, r *http.Request) {
	company, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		api.respondError(w, http.StatusBadRequest, "q is required")
		return
	}
	if len(query) > 250 {
		query = query[:250]
	}
	args := map[string]any{"query": query, "limit": 10}
	if wing := r.URL.Query().Get("wing"); wing != "" {
		args["wing"] = wing
	}
	if room := r.URL.Query().Get("room"); room != "" {
		args["room"] = room
	}
	if limit, _ := strconv.Atoi(r.URL.Query().Get("limit")); limit > 0 && limit <= 50 {
		args["limit"] = limit
	}
	out, callOK := api.memoryCall(w, r, server, "mempalace_search", args)
	if !callOK {
		return
	}
	api.logMemoryActivity(r.Context(), company.ID, "api:search", "read", str(args["wing"]), str(args["room"]), query, 0)
	api.respondRawJSON(w, out)
}

// GetMemoryGraph composes the palace's structural data into a nodes+edges
// document for the UI graph tab: wings and rooms as nodes, wing→room
// containment plus tunnels/hallways as edges.
func (api *API) GetMemoryGraph(w http.ResponseWriter, r *http.Request) {
	_, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}

	type node struct {
		ID          string `json:"id"`
		Label       string `json:"label"`
		Kind        string `json:"kind"` // "wing" | "room"
		DrawerCount int    `json:"drawer_count"`
	}
	type edge struct {
		From  string `json:"from"`
		To    string `json:"to"`
		Kind  string `json:"kind"` // "contains" | "tunnel" | "hallway"
		Label string `json:"label,omitempty"`
	}
	var nodes []node
	var edges []edge

	taxOut, callOK := api.memoryCall(w, r, server, "mempalace_get_taxonomy", map[string]any{})
	if !callOK {
		return
	}
	var tax struct {
		Taxonomy map[string]map[string]int `json:"taxonomy"`
	}
	if err := json.Unmarshal([]byte(taxOut), &tax); err != nil {
		api.respondError(w, http.StatusBadGateway, "unexpected taxonomy payload")
		return
	}
	for wing, rooms := range tax.Taxonomy {
		total := 0
		for room, count := range rooms {
			roomID := wing + "/" + room
			nodes = append(nodes, node{ID: roomID, Label: room, Kind: "room", DrawerCount: count})
			edges = append(edges, edge{From: wing, To: roomID, Kind: "contains"})
			total += count
		}
		nodes = append(nodes, node{ID: wing, Label: wing, Kind: "wing", DrawerCount: total})
	}

	// Tunnels/hallways are best-effort decoration; ignore failures.
	if out, err := mempalace.CallServerTool(r.Context(), server, "mempalace_list_tunnels", map[string]any{}); err == nil {
		var parsed struct {
			Tunnels []struct {
				SourceWing string `json:"source_wing"`
				SourceRoom string `json:"source_room"`
				TargetWing string `json:"target_wing"`
				TargetRoom string `json:"target_room"`
				Label      string `json:"label"`
			} `json:"tunnels"`
		}
		if json.Unmarshal([]byte(out), &parsed) == nil {
			for _, t := range parsed.Tunnels {
				edges = append(edges, edge{
					From: t.SourceWing + "/" + t.SourceRoom, To: t.TargetWing + "/" + t.TargetRoom,
					Kind: "tunnel", Label: t.Label,
				})
			}
		}
	}
	if out, err := mempalace.CallServerTool(r.Context(), server, "mempalace_list_hallways", map[string]any{}); err == nil {
		var parsed struct {
			Hallways []struct {
				Wing       string `json:"wing"`
				SourceRoom string `json:"source_room"`
				TargetRoom string `json:"target_room"`
			} `json:"hallways"`
		}
		if json.Unmarshal([]byte(out), &parsed) == nil {
			for _, h := range parsed.Hallways {
				edges = append(edges, edge{
					From: h.Wing + "/" + h.SourceRoom, To: h.Wing + "/" + h.TargetRoom, Kind: "hallway",
				})
			}
		}
	}

	api.respondJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "edges": edges})
}

func (api *API) ListMemoryDrawers(w http.ResponseWriter, r *http.Request) {
	_, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	args := map[string]any{}
	if wing := r.URL.Query().Get("wing"); wing != "" {
		args["wing"] = wing
	}
	if room := r.URL.Query().Get("room"); room != "" {
		args["room"] = room
	}
	if limit, _ := strconv.Atoi(r.URL.Query().Get("limit")); limit > 0 && limit <= 100 {
		args["limit"] = limit
	}
	if offset, _ := strconv.Atoi(r.URL.Query().Get("offset")); offset > 0 {
		args["offset"] = offset
	}
	if out, ok := api.memoryCall(w, r, server, "mempalace_list_drawers", args); ok {
		api.respondRawJSON(w, out)
	}
}

func (api *API) GetMemoryDrawer(w http.ResponseWriter, r *http.Request) {
	_, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	if out, ok := api.memoryCall(w, r, server, "mempalace_get_drawer",
		map[string]any{"drawer_id": chi.URLParam(r, "drawerID")}); ok {
		api.respondRawJSON(w, out)
	}
}

func (api *API) UpdateMemoryDrawer(w http.ResponseWriter, r *http.Request) {
	company, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Content) == "" {
		api.respondError(w, http.StatusBadRequest, "content is required")
		return
	}
	drawerID := chi.URLParam(r, "drawerID")
	out, callOK := api.memoryCall(w, r, server, "mempalace_update_drawer",
		map[string]any{"drawer_id": drawerID, "content": req.Content})
	if !callOK {
		return
	}
	api.logMemoryActivity(r.Context(), company.ID, "api:update-drawer", "write", "", "", drawerID, 1)
	api.respondRawJSON(w, out)
}

func (api *API) DeleteMemoryDrawer(w http.ResponseWriter, r *http.Request) {
	company, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	drawerID := chi.URLParam(r, "drawerID")
	out, callOK := api.memoryCall(w, r, server, "mempalace_delete_drawer", map[string]any{"drawer_id": drawerID})
	if !callOK {
		return
	}
	api.logMemoryActivity(r.Context(), company.ID, "api:delete-drawer", "write", "", "", drawerID, 1)
	api.respondRawJSON(w, out)
}

// SupersedeMemoryDrawer marks a drawer superseded (the safe default
// correction — history stays recallable but reads as outdated).
func (api *API) SupersedeMemoryDrawer(w http.ResponseWriter, r *http.Request) {
	company, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	drawerID := chi.URLParam(r, "drawerID")

	full, callOK := api.memoryCall(w, r, server, "mempalace_get_drawer", map[string]any{"drawer_id": drawerID})
	if !callOK {
		return
	}
	var drawer struct {
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(full), &drawer) != nil || drawer.Content == "" {
		api.respondError(w, http.StatusNotFound, "drawer not found")
		return
	}
	marker := "[SUPERSEDED"
	if strings.HasPrefix(drawer.Content, marker) {
		api.respondJSON(w, http.StatusOK, map[string]string{"status": "already superseded"})
		return
	}
	prefix := "[SUPERSEDED]"
	if req.Reason != "" {
		prefix = "[SUPERSEDED: " + req.Reason + "]"
	}
	out, callOK := api.memoryCall(w, r, server, "mempalace_update_drawer",
		map[string]any{"drawer_id": drawerID, "content": prefix + " " + drawer.Content})
	if !callOK {
		return
	}
	api.logMemoryActivity(r.Context(), company.ID, "api:supersede-drawer", "write", "", "", drawerID, 1)
	api.respondRawJSON(w, out)
}

func (api *API) GetMemoryFacts(w http.ResponseWriter, r *http.Request) {
	_, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	entity := strings.TrimSpace(r.URL.Query().Get("entity"))
	if entity == "" {
		// No entity → full timeline gives the complete fact list.
		if out, ok := api.memoryCall(w, r, server, "mempalace_kg_timeline", map[string]any{}); ok {
			api.respondRawJSON(w, out)
		}
		return
	}
	args := map[string]any{"entity": entity}
	if asOf := r.URL.Query().Get("as_of"); asOf != "" {
		args["as_of"] = asOf
	}
	if out, ok := api.memoryCall(w, r, server, "mempalace_kg_query", args); ok {
		api.respondRawJSON(w, out)
	}
}

func (api *API) GetMemoryFactTimeline(w http.ResponseWriter, r *http.Request) {
	_, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	args := map[string]any{}
	if entity := r.URL.Query().Get("entity"); entity != "" {
		args["entity"] = entity
	}
	if out, ok := api.memoryCall(w, r, server, "mempalace_kg_timeline", args); ok {
		api.respondRawJSON(w, out)
	}
}

func (api *API) AddMemoryFact(w http.ResponseWriter, r *http.Request) {
	company, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	var req struct {
		Subject   string `json:"subject"`
		Predicate string `json:"predicate"`
		Object    string `json:"object"`
		ValidFrom string `json:"valid_from"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Subject == "" || req.Predicate == "" || req.Object == "" {
		api.respondError(w, http.StatusBadRequest, "subject, predicate and object are required")
		return
	}
	args := map[string]any{"subject": req.Subject, "predicate": req.Predicate, "object": req.Object}
	if req.ValidFrom != "" {
		args["valid_from"] = req.ValidFrom
	}
	out, callOK := api.memoryCall(w, r, server, "mempalace_kg_add", args)
	if !callOK {
		return
	}
	api.logMemoryActivity(r.Context(), company.ID, "api:add-fact", "write", "", "",
		req.Subject+" "+req.Predicate+" "+req.Object, 1)
	api.respondRawJSON(w, out)
}

func (api *API) InvalidateMemoryFact(w http.ResponseWriter, r *http.Request) {
	company, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	var req struct {
		Subject   string `json:"subject"`
		Predicate string `json:"predicate"`
		Object    string `json:"object"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Subject == "" || req.Predicate == "" || req.Object == "" {
		api.respondError(w, http.StatusBadRequest, "subject, predicate and object are required")
		return
	}
	out, callOK := api.memoryCall(w, r, server, "mempalace_kg_invalidate",
		map[string]any{"subject": req.Subject, "predicate": req.Predicate, "object": req.Object})
	if !callOK {
		return
	}
	api.logMemoryActivity(r.Context(), company.ID, "api:invalidate-fact", "write", "", "",
		req.Subject+" "+req.Predicate+" "+req.Object, 1)
	api.respondRawJSON(w, out)
}

// ListMemoryActivityFeed serves the activity feed from the MemoryActivity
// table (works even while the palace itself is unavailable).
func (api *API) ListMemoryActivityFeed(w http.ResponseWriter, r *http.Request) {
	companyID, _ := strconv.Atoi(r.URL.Query().Get("company_id"))
	if companyID == 0 {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	rows, total, err := api.q.ListMemoryActivity(r.Context(), int32(companyID),
		r.URL.Query().Get("agent"), r.URL.Query().Get("kind"), limit, offset)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]any{"items": rows, "total": total})
}

// GetMemoryAgents returns per-agent memory usage aggregates plus each
// agent's recent diary entries.
func (api *API) GetMemoryAgents(w http.ResponseWriter, r *http.Request) {
	company, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	stats, err := api.q.MemoryAgentStats(r.Context(), company.ID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type agentEntry struct {
		db.MemoryAgentStat
		Diary json.RawMessage `json:"diary,omitempty"`
	}
	out := make([]agentEntry, 0, len(stats))
	for _, s := range stats {
		entry := agentEntry{MemoryAgentStat: s}
		if diary, dErr := mempalace.CallServerTool(r.Context(), server, "mempalace_diary_read",
			map[string]any{"agent_name": mempalace.SanitizeName(s.AgentName), "last_n": 5}); dErr == nil && json.Valid([]byte(diary)) {
			entry.Diary = json.RawMessage(diary)
		}
		out = append(out, entry)
	}
	api.respondJSON(w, http.StatusOK, out)
}

// RunMemoryMaintenance kicks a maintenance job: "mine" (re-index a project
// workspace, async) or "sync" (prune drawers for deleted sources, dry-run
// unless apply=true).
func (api *API) RunMemoryMaintenance(w http.ResponseWriter, r *http.Request) {
	company, server, ok := api.memoryCompany(w, r)
	if !ok {
		return
	}
	job := chi.URLParam(r, "job")
	switch job {
	case "mine":
		var req struct {
			ProjectID int32 `json:"project_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ProjectID == 0 {
			api.respondError(w, http.StatusBadRequest, "project_id is required")
			return
		}
		project, err := api.q.GetProject(r.Context(), req.ProjectID)
		if err != nil || project.CompanyID != company.ID {
			api.respondError(w, http.StatusNotFound, "project not found")
			return
		}
		settings := LoadSettings()
		workspaceDir := filesystem.NewManager(settings.BasePath).GetProjectRepoPath(company, project)
		mempalace.MineProjectAsync(company, project, workspaceDir)
		api.logMemoryActivity(r.Context(), company.ID, "api:mine", "maintenance", mempalace.ProjectWing(project.Name), "", project.Name, 0)
		api.respondJSON(w, http.StatusOK, map[string]string{"status": "mining started"})
	case "sync":
		apply := r.URL.Query().Get("apply") == "true"
		out, callOK := api.memoryCall(w, r, server, "mempalace_sync", map[string]any{"apply": apply})
		if !callOK {
			return
		}
		api.logMemoryActivity(r.Context(), company.ID, "api:sync", "maintenance", "", "", "", 0)
		api.respondRawJSON(w, out)
	default:
		api.respondError(w, http.StatusBadRequest, "unknown job — use mine or sync")
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// EnsureCompanyPalaces provisions a memory palace (dir + MCP server row) for
// every company. Called at startup after the setup script finishes, and safe
// to call repeatedly. No-op while mempalace is unavailable.
func (api *API) EnsureCompanyPalaces(ctx context.Context) {
	if !mempalace.Available() {
		return
	}
	companies, err := api.q.ListCompanies(ctx)
	if err != nil {
		return
	}
	for _, company := range companies {
		if _, err := mempalace.EnsureCompanyServer(ctx, api.q, company); err != nil {
			continue
		}
	}
}
