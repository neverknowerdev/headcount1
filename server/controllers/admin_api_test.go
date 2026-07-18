package endpoints

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequireGlobalAdminAPIGate(t *testing.T) {
	api := &API{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := api.RequireGlobalAdminAPI(next)

	call := func() int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/backup/status", nil))
		return w.Code
	}

	// Default (opt-in unset, not E2E): the route is hidden.
	require.Equal(t, http.StatusNotFound, call())

	// Operator opt-in exposes it.
	t.Setenv("HEADCOUNT1_ENABLE_GLOBAL_ADMIN_API", "1")
	require.Equal(t, http.StatusOK, call())
}
