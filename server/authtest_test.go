package server

import (
	"context"
	"net/http"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testSeedUserEmail = "seed@test.local"

// testSeedUserID returns (creating if needed) the fixture user every server
// test seeds data under, now that all rows are tenant-scoped.
func testSeedUserID(t *testing.T, q *db.Queries) int32 {
	t.Helper()
	u, err := q.GetUserByEmail(context.Background(), testSeedUserEmail)
	if err != nil {
		u, err = q.CreateUser(context.Background(), testSeedUserEmail, "not-a-real-hash")
		require.NoError(t, err)
	}
	return u.ID
}

// withTestUser wraps a router in middleware that injects the fixture user
// into every request's context — the test-side stand-in for RequireAuth.
func withTestUser(t *testing.T, database *gorm.DB, r chi.Router) chi.Router {
	t.Helper()
	q := db.New(database)
	uid := testSeedUserID(t, q)
	user, err := q.GetUser(context.Background(), uid)
	require.NoError(t, err)

	wrapped := chi.NewRouter()
	wrapped.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUser(req.Context(), user)))
		})
	})
	wrapped.Mount("/", r)
	return wrapped
}
