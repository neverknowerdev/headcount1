package endpoints

import (
	"encoding/json"
	"net/http"
	"strconv"

	"agent-orchestrator/pkg/mempalace"
)

// GetMemoryTaxonomy returns the full palace hierarchy (wings → rooms → counts).
func (api *API) GetMemoryTaxonomy(w http.ResponseWriter, r *http.Request) {
	client, err := mempalace.NewClient(r.Context())
	if err != nil || client == nil {
		api.respondJSON(w, http.StatusOK, map[string]any{"available": false, "data": nil})
		return
	}
	defer client.Close()

	raw, err := client.GetTaxonomy(r.Context())
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to get taxonomy: "+err.Error())
		return
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		api.respondJSON(w, http.StatusOK, map[string]any{"available": true, "data": raw})
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]any{"available": true, "data": parsed})
}

// ListMemoryDrawers returns drawers filtered by wing/room with pagination.
func (api *API) ListMemoryDrawers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wing := q.Get("wing")
	room := q.Get("room")
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 {
		limit = 20
	}

	client, err := mempalace.NewClient(r.Context())
	if err != nil || client == nil {
		api.respondJSON(w, http.StatusOK, map[string]any{"drawers": []any{}, "total": 0})
		return
	}
	defer client.Close()

	raw, err := client.ListDrawers(r.Context(), wing, room, limit, offset)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to list drawers: "+err.Error())
		return
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		api.respondJSON(w, http.StatusOK, map[string]any{"drawers": []any{}, "total": 0})
		return
	}
	api.respondJSON(w, http.StatusOK, parsed)
}

// GetMemoryDrawer fetches a single drawer by ID (query param ?id=...).
func (api *API) GetMemoryDrawer(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		api.respondError(w, http.StatusBadRequest, "id query parameter required")
		return
	}

	client, err := mempalace.NewClient(r.Context())
	if err != nil || client == nil {
		api.respondError(w, http.StatusServiceUnavailable, "mempalace not available")
		return
	}
	defer client.Close()

	raw, err := client.GetDrawer(r.Context(), id)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to get drawer: "+err.Error())
		return
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to parse response")
		return
	}
	api.respondJSON(w, http.StatusOK, parsed)
}
