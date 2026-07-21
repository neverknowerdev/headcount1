package endpoints

import (
	"net/http"
	"os"
	"strings"

	"agent-orchestrator/pkg/utils"
)

// globalAdminAPIEnabled reports whether the instance-global admin operations
// (server-wide backup/restore, global settings) are exposed over HTTP. They are
// OFF by default: these routes act on the whole multi-tenant instance (a restore
// wipes and replaces every tenant's data), so in a shared deployment they must
// not be reachable by an arbitrary self-registered user. An operator opts in
// with HEADCOUNT1_ENABLE_GLOBAL_ADMIN_API on a single-tenant/self-hosted box.
func globalAdminAPIEnabled() bool {
	switch strings.ToLower(os.Getenv("HEADCOUNT1_ENABLE_GLOBAL_ADMIN_API")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// RequireGlobalAdminAPI gates instance-global operations. When the operator has
// not opted in (and outside E2E), the routes respond 404 — hidden rather than
// merely forbidden, so their existence isn't advertised. Scheduled server-side
// backups are unaffected; this only governs the HTTP surface.
func (api *API) RequireGlobalAdminAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if utils.IsE2E() || globalAdminAPIEnabled() {
			next.ServeHTTP(w, r)
			return
		}
		api.respondError(w, http.StatusNotFound, "not found")
	})
}
