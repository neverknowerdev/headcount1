package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/secrets"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// unlockForTest puts a deterministic DEK in the keyring so a fixture user can
// seal/open their own secrets (the passkey-unlock a real login would perform).
func unlockForTest(uid int32) {
	var dek [32]byte
	dek[0], dek[1] = byte(uid), 0x5e
	secrets.Default().UnlockUser(uid, dek, time.Hour)
}

// sealForUserTest seals a secret under an (already unlocked) user's DEK — the
// sealed column refuses raw plaintext, so fixtures must pre-seal exactly as the
// controllers do.
func sealForUserTest(uid int32, plaintext string) string {
	v, err := secrets.Default().EncryptForUser(uid, plaintext)
	if err != nil {
		panic(err)
	}
	return v
}

// sealTest seals an ownerless secret under the root DEK for fixtures.
func sealTest(plaintext string) string {
	v, err := secrets.Default().Encrypt(plaintext)
	if err != nil {
		panic(err)
	}
	return v
}

// postJSON POSTs a JSON body to the router, optionally with a session cookie.
func postJSON(t *testing.T, r chi.Router, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// recordingMailer captures the emails a flow would send, for assertions.
type recordingMailer struct {
	mu   sync.Mutex
	sent []struct{ To, Subject, Body string }
}

func (m *recordingMailer) Send(to, subject, textBody string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, struct{ To, Subject, Body string }{to, subject, textBody})
	return nil
}

const testSeedUserEmail = "seed@test.local"

// testSeedUserID returns (creating if needed) the fixture user every server
// test seeds data under, now that all rows are tenant-scoped.
func testSeedUserID(t *testing.T, q *db.Queries) int32 {
	t.Helper()
	u, err := q.GetUserByEmail(context.Background(), testSeedUserEmail)
	if err != nil {
		u, err = q.CreateUser(context.Background(), testSeedUserEmail)
		require.NoError(t, err)
	}
	unlockForTest(u.ID)
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
