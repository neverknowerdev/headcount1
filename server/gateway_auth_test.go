package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"agent-orchestrator/db"
	"agent-orchestrator/integration"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/runtokens"
)

func setupGatewayAuthDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(
		&db.User{}, &db.Session{}, &db.Team{}, &db.TeamMember{},
		&db.Company{}, &db.LLMProvider{}, &db.Agent{}, &db.ProxyRequestLog{},
		&db.ModelGroup{}, &db.ModelGroupMember{}, &db.ModelRequestStat{},
		&db.Task{}, &db.Run{},
	))
	return database
}

// TestGatewayRequiresRunTokenOrSession verifies the machine routes reject
// anonymous callers once enforcement is wired, accept engine-issued run
// tokens, bind X-Run-ID to the token's run, and allow session-cookie callers
// only for resources they own.
func TestGatewayRequiresRunTokenOrSession(t *testing.T) {
	database := setupGatewayAuthDB(t)
	q := db.New(database)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	// Provider owned by user 1; a model group owned by user 1 as well.
	owner, err := q.CreateUser(t.Context(), "owner@x.io")
	require.NoError(t, err)
	other, err := q.CreateUser(t.Context(), "other@x.io")
	require.NoError(t, err)
	unlockForTest(owner.ID)
	provider := db.LLMProvider{Name: "P", BaseUrl: upstream.URL, ApiKey: "k", UserID: &owner.ID}
	require.NoError(t, database.Create(&provider).Error)
	group := db.ModelGroup{Name: "G", Slug: "g", UserID: &owner.ID}
	require.NoError(t, database.Create(&group).Error)
	require.NoError(t, database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: provider.ID, Model: "m", IsFree: true}).Error)

	registry := runtokens.NewRegistry()
	gw := integration.NewLLMGateway(database)
	gw.SetRunTokenValidator(registry.Validate)
	r := chi.NewRouter()
	gw.Mount(r)

	post := func(path, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if mutate != nil {
			mutate(req)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	chatBody := `{"model":"m","stream":false}`

	// 1. Anonymous → 401 on every machine route.
	require.Equal(t, http.StatusUnauthorized, post("/proxy/group/g/v1/chat/completions", chatBody, nil).Code)
	require.Equal(t, http.StatusUnauthorized, post("/v1/chat/completions", chatBody, func(r *http.Request) {
		r.Header.Set("X-Provider-ID", "1")
	}).Code)

	// 2. A valid run token passes.
	token := registry.Issue(42)
	w := post("/proxy/group/g/v1/chat/completions", chatBody, func(r *http.Request) {
		r.Header.Set(runtokens.TokenHeader, token)
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 3. X-Run-ID must match the token's run (no cross-run log poisoning).
	w = post("/proxy/group/g/v1/chat/completions", chatBody, func(r *http.Request) {
		r.Header.Set(runtokens.TokenHeader, token)
		r.Header.Set("X-Run-ID", "999")
	})
	require.Equal(t, http.StatusForbidden, w.Code)

	// 4. A revoked token stops working (the run ended).
	registry.Revoke(42)
	w = post("/proxy/group/g/v1/chat/completions", chatBody, func(r *http.Request) {
		r.Header.Set(runtokens.TokenHeader, token)
	})
	require.Equal(t, http.StatusUnauthorized, w.Code)

	// 5. Session cookies work — but only for resources the user owns.
	newSessionCookie := func(userID int32) *http.Cookie {
		raw, err := authctx.NewToken()
		require.NoError(t, err)
		_, err = q.CreateSession(t.Context(), userID, authctx.HashToken(raw))
		require.NoError(t, err)
		return &http.Cookie{Name: authctx.CookieName, Value: raw}
	}
	ownerCookie := newSessionCookie(owner.ID)
	otherCookie := newSessionCookie(other.ID)

	w = post("/proxy/group/g/v1/chat/completions", chatBody, func(r *http.Request) { r.AddCookie(ownerCookie) })
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = post("/proxy/group/g/v1/chat/completions", chatBody, func(r *http.Request) { r.AddCookie(otherCookie) })
	require.Equal(t, http.StatusNotFound, w.Code, "another tenant's group must look nonexistent")

	w = post("/v1/chat/completions", chatBody, func(r *http.Request) {
		r.AddCookie(otherCookie)
		r.Header.Set("X-Provider-ID", "1")
	})
	require.Equal(t, http.StatusNotFound, w.Code, "another tenant's provider must look nonexistent")
}

// TestGatewayOpenWithoutValidator pins the legacy/local behavior: with no
// validator wired (unit tests, stripped-down embeddings), the gateway stays
// open exactly as before.
func TestGatewayOpenWithoutValidator(t *testing.T) {
	database := setupGatewayAuthDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	provider := db.LLMProvider{Name: "P", BaseUrl: upstream.URL, ApiKey: "k"}
	require.NoError(t, database.Create(&provider).Error)
	group := db.ModelGroup{Name: "G", Slug: "g"}
	require.NoError(t, database.Create(&group).Error)
	require.NoError(t, database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: provider.ID, Model: "m", IsFree: true}).Error)

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	req := httptest.NewRequest("POST", "/proxy/group/g/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}
