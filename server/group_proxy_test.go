package server_test

import (
	"agent-orchestrator/db/migrations"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/integration"
	"agent-orchestrator/pkg/secrets"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// sealTestUserID is a fixed user kept unlocked for the whole test process, so
// tests can seal a provider API key (the write guard refuses raw plaintext) and
// have it decrypt at the point of use via the in-memory keyring.
const sealTestUserID = 424242

// sealKey seals a provider API key under sealTestUserID. Ciphertext embeds that
// user id ("enc:u1:<id>:…"), so Decrypt opens it as long as the user is unlocked
// — which this helper guarantees.
func sealKey(s string) string {
	var dek [32]byte
	dek[0], dek[1] = 0x42, 0x42
	secrets.Default().UnlockUser(sealTestUserID, dek, time.Hour)
	v, err := secrets.Default().EncryptForUser(sealTestUserID, s)
	if err != nil {
		panic(err)
	}
	return v
}

func setupGroupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// One connection only: every pooled connection to a ":memory:" SQLite
	// DSN would otherwise get its own empty database.
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := migrations.ApplyGORM(database, "sqlite", "test"); err != nil {
		t.Fatal(err)
	}
	return database
}

func groupRequest(t *testing.T, r chi.Router, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/proxy/group/"+key+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("group_key", key)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestGroupProxyFailover: first member is rate limited (429), the router
// must fail over to the second member, return its response, and record one
// rate-limited stat row and one success stat row.
func TestGroupProxyFailover(t *testing.T) {
	database := setupGroupTestDB(t)

	var failCalls, okCalls int
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failCalls++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error": {"message": "Rate limit exceeded", "code": 429}}`))
	}))
	defer failServer.Close()

	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okCalls++
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		json.Unmarshal(body, &payload)
		if payload["model"] != "backup-model" {
			t.Errorf("expected rewritten model backup-model, got %v", payload["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices": [{"message": {"content": "hi"}}], "usage": {"prompt_tokens": 4, "completion_tokens": 8, "total_tokens": 12}}`))
	}))
	defer okServer.Close()

	p1 := db.LLMProvider{Name: "Limited", BaseUrl: failServer.URL, ApiKeyEncrypted: sealKey("k1")}
	p2 := db.LLMProvider{Name: "Backup", BaseUrl: okServer.URL, ApiKeyEncrypted: sealKey("k2")}
	database.Create(&p1)
	database.Create(&p2)

	group := db.ModelGroup{Name: "Test Group", Slug: "test-group"}
	database.Create(&group)
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p1.ID, Model: "primary-model", IsFree: true, Priority: 0})
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p2.ID, Model: "backup-model", IsFree: true, Priority: 1})

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	w := groupRequest(t, r, "test-group", `{"model": "whatever", "stream": false}`)
	if w.Code != 200 {
		t.Fatalf("expected 200 after failover, got %d: %s", w.Code, w.Body.String())
	}
	if failCalls != 1 || okCalls != 1 {
		t.Fatalf("expected 1 call to each provider, got fail=%d ok=%d", failCalls, okCalls)
	}

	time.Sleep(100 * time.Millisecond)

	var stats []db.ModelRequestStat
	database.Order("id").Find(&stats)
	if len(stats) != 2 {
		t.Fatalf("expected 2 stat rows, got %d", len(stats))
	}
	if !stats[0].RateLimited || stats[0].Success {
		t.Errorf("first attempt should be rate-limited, got %+v", stats[0])
	}
	if stats[0].CooldownUntil == nil || !stats[0].CooldownUntil.After(time.Now()) {
		t.Errorf("rate-limited stat should carry a future cooldown, got %v", stats[0].CooldownUntil)
	}
	if !stats[1].Success || stats[1].RateLimited {
		t.Errorf("second attempt should be a success, got %+v", stats[1])
	}
	if stats[1].CompletionTokens != 8 || stats[1].TokensPerSec <= 0 {
		t.Errorf("success stat should have tokens and tokens/sec, got %+v", stats[1])
	}

	// The rate-limited member is now in cooldown: the next request must go
	// straight to the backup without touching the limited provider.
	w2 := groupRequest(t, r, "test-group", `{"model": "whatever", "stream": false}`)
	if w2.Code != 200 {
		t.Fatalf("expected 200, got %d", w2.Code)
	}
	if failCalls != 1 {
		t.Errorf("cooled-down provider should be skipped, but was called %d times", failCalls)
	}
	if okCalls != 2 {
		t.Errorf("expected backup to serve second request, calls=%d", okCalls)
	}
}

// TestGroupProxyFreeBeforePaid: paid members must only be used after free
// members fail, regardless of priority order.
func TestGroupProxyFreeBeforePaid(t *testing.T) {
	database := setupGroupTestDB(t)

	var order []string
	mk := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, name)
			if name == "free" {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error": {"message": "boom"}}`))
				return
			}
			w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}], "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}`))
		}))
	}
	freeServer := mk("free")
	paidServer := mk("paid")
	defer freeServer.Close()
	defer paidServer.Close()

	pPaid := db.LLMProvider{Name: "Paid", BaseUrl: paidServer.URL, ApiKeyEncrypted: sealKey("k")}
	pFree := db.LLMProvider{Name: "Free", BaseUrl: freeServer.URL, ApiKeyEncrypted: sealKey("k")}
	database.Create(&pPaid)
	database.Create(&pFree)

	group := db.ModelGroup{Name: "Tier Group", Slug: "tier-group"}
	database.Create(&group)
	// Paid member has better priority — free must still win the first slot.
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: pPaid.ID, Model: "paid-model", IsFree: false, Priority: 0})
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: pFree.ID, Model: "free-model", IsFree: true, Priority: 1})

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	w := groupRequest(t, r, "tier-group", `{"model": "x"}`)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(order) != 2 || order[0] != "free" || order[1] != "paid" {
		t.Fatalf("expected free tried before paid, got %v", order)
	}
}

// TestGroupProxyAllFail: when every member fails the client gets a JSON
// error with the last upstream status.
func TestGroupProxyAllFail(t *testing.T) {
	database := setupGroupTestDB(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": {"message": "down"}}`))
	}))
	defer srv.Close()

	p := db.LLMProvider{Name: "Down", BaseUrl: srv.URL, ApiKeyEncrypted: sealKey("k")}
	database.Create(&p)
	group := db.ModelGroup{Name: "Down Group", Slug: "down-group"}
	database.Create(&group)
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p.ID, Model: "m1", IsFree: true})

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	w := groupRequest(t, r, "down-group", `{"model": "x"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "model_group_exhausted") {
		t.Fatalf("expected model_group_exhausted error, got %s", w.Body.String())
	}
}

// TestGroupProxyVaultLocked: when every member's key is sealed under a locked
// vault, the router answers 423 vault_locked — distinct from a model failure —
// without ever calling upstream.
func TestGroupProxyVaultLocked(t *testing.T) {
	database := setupGroupTestDB(t)

	var upstreamCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	const lockedOwner int32 = 515151
	var dek [32]byte
	dek[0] = 0x51
	secrets.Default().UnlockUser(lockedOwner, dek, time.Hour)
	sealed, err := secrets.Default().EncryptForUser(lockedOwner, "k")
	if err != nil {
		t.Fatal(err)
	}
	owner := lockedOwner
	p := db.LLMProvider{Name: "Sealed", BaseUrl: srv.URL, ApiKeyEncrypted: sealed, UserID: &owner}
	database.Create(&p)
	group := db.ModelGroup{Name: "Sealed Group", Slug: "sealed-group"}
	database.Create(&group)
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p.ID, Model: "m1", IsFree: true})
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p.ID, Model: "m2", IsFree: true})

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	secrets.Default().LockUser(lockedOwner)
	w := groupRequest(t, r, "sealed-group", `{"model": "x"}`)
	if w.Code != http.StatusLocked {
		t.Fatalf("expected 423, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), integration.VaultLockedErrorType) {
		t.Fatalf("expected vault_locked error, got %s", w.Body.String())
	}
	if n := atomic.LoadInt32(&upstreamCalls); n != 0 {
		t.Fatalf("a locked vault must not reach upstream, got %d calls", n)
	}

	// Once the owner unlocks, the same request succeeds.
	secrets.Default().UnlockUser(lockedOwner, dek, time.Hour)
	w = groupRequest(t, r, "sealed-group", `{"model": "x"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after unlock, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGroupProxyLogsOnlyModelSwitchesForARun: for a request made on behalf of
// a run, the router adds its model switches to the run's log and nothing
// else. The session logs its own requests and responses and accounts for its
// own tokens; the router doing either as well would double them.
func TestGroupProxyLogsOnlyModelSwitchesForARun(t *testing.T) {
	database := setupGroupTestDB(t)

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": {"message": "boom"}}`))
	}))
	defer failServer.Close()
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}], "usage": {"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}}`))
	}))
	defer okServer.Close()

	comp := db.Company{Name: "T", ShortName: "t"}
	database.Create(&comp)
	sprint := db.Sprint{CompanyID: comp.ID, Name: "S"}
	database.Create(&sprint)
	p1 := db.LLMProvider{Name: "Bad", BaseUrl: failServer.URL, ApiKeyEncrypted: sealKey("k")}
	p2 := db.LLMProvider{Name: "Good", BaseUrl: okServer.URL, ApiKeyEncrypted: sealKey("k")}
	database.Create(&p1)
	database.Create(&p2)
	group := db.ModelGroup{Name: "Run Group", Slug: "run-group"}
	database.Create(&group)
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p1.ID, Model: "m1", IsFree: true, Priority: 0})
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p2.ID, Model: "m2", IsFree: true, Priority: 1})
	agent := db.Agent{CompanyID: comp.ID, Name: "A"}
	database.Create(&agent)
	task := db.Task{CompanyID: comp.ID, SprintID: sprint.ID, AgentID: &agent.ID, Title: "T"}
	database.Create(&task)
	run := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running"}
	database.Create(&run)

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	req := httptest.NewRequest("POST", "/proxy/group/run-group/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Run-ID", "1")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("group_key", "run-group")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	time.Sleep(200 * time.Millisecond)

	var reloaded db.Run
	database.First(&reloaded, run.ID)
	var entries []map[string]interface{}
	json.Unmarshal([]byte(reloaded.LogEntries), &entries)
	var switches, other int
	for _, e := range entries {
		switch e["type"] {
		case "model_switch":
			switches++
		case "request", "response":
			other++
		}
	}
	if switches != 1 {
		t.Errorf("expected 1 model_switch entry, got %d (entries: %s)", switches, reloaded.LogEntries)
	}
	if other != 0 {
		t.Errorf("the router must not log request/response entries, got %d", other)
	}
	if reloaded.TokenStats != "" && reloaded.TokenStats != "{}" {
		t.Errorf("the router must not account for a run's tokens, got %s", reloaded.TokenStats)
	}
}

// TestGroupProxyAllModelsMember: a member with AllModels=true expands to one
// candidate per model in its provider's SupportedModels at request time, so
// routing works without enumerating every model by hand.
func TestGroupProxyAllModelsMember(t *testing.T) {
	database := setupGroupTestDB(t)

	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		json.Unmarshal(body, &payload)
		gotModel, _ = payload["model"].(string)
		w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}], "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}`))
	}))
	defer srv.Close()

	p := db.LLMProvider{Name: "AnyProvider", BaseUrl: srv.URL, ApiKeyEncrypted: sealKey("k"), SupportedModels: "model-a,model-b"}
	database.Create(&p)
	group := db.ModelGroup{Name: "Any Group", Slug: "any-group"}
	database.Create(&group)
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p.ID, AllModels: true, IsFree: true})

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	w := groupRequest(t, r, "any-group", `{"model": "x"}`)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotModel != "model-a" && gotModel != "model-b" {
		t.Fatalf("expected the request to use one of the provider's models, got %q", gotModel)
	}

	// /v1/models lists the expanded concrete models, not the wildcard.
	modelsReq := httptest.NewRequest("GET", "/proxy/group/any-group/v1/models", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("group_key", "any-group")
	modelsReq = modelsReq.WithContext(context.WithValue(modelsReq.Context(), chi.RouteCtxKey, rctx))
	mw := httptest.NewRecorder()
	r.ServeHTTP(mw, modelsReq)
	if !strings.Contains(mw.Body.String(), "model-a") || !strings.Contains(mw.Body.String(), "model-b") {
		t.Fatalf("expected /v1/models to list expanded models, got %s", mw.Body.String())
	}
}

// TestGroupProxyAllModelsMember_TriesEveryModelInOrder: with an AllModels
// wildcard member, the router must walk the provider's ENTIRE model list —
// not just one pick — trying each concrete model in turn (in the order
// SupportedModels lists them) until one succeeds, exactly like a group with
// that many explicit members.
func TestGroupProxyAllModelsMember_TriesEveryModelInOrder(t *testing.T) {
	database := setupGroupTestDB(t)

	var triedModels []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		json.Unmarshal(body, &payload)
		model, _ := payload["model"].(string)
		triedModels = append(triedModels, model)
		if model == "model-c" {
			w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}], "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": {"message": "boom"}}`))
	}))
	defer srv.Close()

	p := db.LLMProvider{Name: "AnyProvider", BaseUrl: srv.URL, ApiKeyEncrypted: sealKey("k"), SupportedModels: "model-a,model-b,model-c"}
	database.Create(&p)
	group := db.ModelGroup{Name: "Any Group", Slug: "any-group"}
	database.Create(&group)
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p.ID, AllModels: true, IsFree: true})

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	w := groupRequest(t, r, "any-group", `{"model": "x"}`)
	if w.Code != 200 {
		t.Fatalf("expected 200 once model-c succeeds, got %d: %s", w.Code, w.Body.String())
	}
	if len(triedModels) != 3 {
		t.Fatalf("expected all 3 models to be tried in order before success, got %v", triedModels)
	}
	if triedModels[0] != "model-a" || triedModels[1] != "model-b" || triedModels[2] != "model-c" {
		t.Fatalf("expected model-a, model-b, model-c in that order, got %v", triedModels)
	}

	time.Sleep(50 * time.Millisecond)
	var stats []db.ModelRequestStat
	database.Find(&stats)
	if len(stats) != 3 {
		t.Fatalf("expected 3 stat rows (one per attempted model), got %d", len(stats))
	}
	// Each attempt's stat row is persisted in its own goroutine, so rows can
	// land in any order — match by model instead of assuming insertion order.
	byModel := map[string]db.ModelRequestStat{}
	for _, s := range stats {
		byModel[s.Model] = s
	}
	for _, model := range []string{"model-a", "model-b"} {
		s, ok := byModel[model]
		if !ok || s.Success {
			t.Errorf("expected a failed attempt on %s, got %+v (present=%v)", model, s, ok)
		}
	}
	if s, ok := byModel["model-c"]; !ok || !s.Success {
		t.Errorf("expected a success stat row on model-c, got %+v (present=%v)", s, ok)
	}
}

// A group of System One models routes a systemone call between its members
// just as a group of language models routes a chat completion: free members
// first, failover on a rate limit, the model swapped for the member's own, a
// statistic for each attempt.
func TestGroupProxyRoutesSystemOneCalls(t *testing.T) {
	database := setupGroupTestDB(t)

	var limitedCalls, okCalls int
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limitedCalls++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error": {"message": "Rate limit exceeded"}}`))
	}))
	defer limited.Close()
	var servedPath, servedModel, servedSession string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okCalls++
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		json.Unmarshal(body, &payload)
		servedPath, servedModel, servedSession = r.URL.Path, payload["model"].(string), r.Header.Get("x-opencode-session")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model": "jev-1.13", "answers": {"urgent": {"noul": 0.9}}, "usage": {"input_tokens": 40, "output_tokens": 2}}`))
	}))
	defer ok.Close()

	free := db.LLMProvider{Name: "Free", BaseUrl: limited.URL + "/v1", ApiKeyEncrypted: sealKey("k1"), SystemOneModels: "jev-1.13-free"}
	paid := db.LLMProvider{Name: "Paid", BaseUrl: ok.URL + "/v1", ApiKeyEncrypted: sealKey("k2"), SupportedModels: "big-pickle", SystemOneModels: "jev-1.13"}
	database.Create(&free)
	database.Create(&paid)
	group := db.ModelGroup{Name: "Classifiers", Slug: "classifiers", Kind: db.ModelKindSystemOne}
	database.Create(&group)
	// The paid member comes first in the list; the free one is still tried first.
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: paid.ID, AllModels: true, Priority: 0})
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: free.ID, Model: "jev-1.13-free", IsFree: true, Priority: 1})
	writers := db.ModelGroup{Name: "Writers", Slug: "writers"}
	database.Create(&writers)
	database.Create(&db.ModelGroupMember{GroupID: writers.ID, ProviderID: paid.ID, Model: "big-pickle"})

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)
	post := func(key, endpoint, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/proxy/group/"+key+"/v1/"+endpoint, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-opencode-session", "hc1-test")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("group_key", key)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := post("classifiers", "systemone", `{"model": "classifiers", "state": "The server is down.", "questions": {"urgent": {"type": "noul", "instructions": "Is it urgent?"}}}`)
	if w.Code != 200 {
		t.Fatalf("expected 200 after failover, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"noul": 0.9`) {
		t.Errorf("the answer should pass through unchanged, got %s", w.Body.String())
	}
	if limitedCalls != 1 || okCalls != 1 {
		t.Fatalf("expected the free member first, then the paid one; got free=%d paid=%d", limitedCalls, okCalls)
	}
	if servedPath != "/v1/systemone" || servedModel != "jev-1.13" {
		t.Errorf("expected the paid provider's own System One model at its systemone endpoint, got %s %s", servedPath, servedModel)
	}
	if servedSession != "hc1-test" {
		t.Errorf("the session header should reach the provider, got %q", servedSession)
	}

	time.Sleep(100 * time.Millisecond)
	var stats []db.ModelRequestStat
	database.Order("id").Find(&stats)
	if len(stats) != 2 || !stats[0].RateLimited || !stats[1].Success {
		t.Fatalf("expected a rate-limited attempt and a success, got %+v", stats)
	}
	if stats[1].PromptTokens != 40 || stats[1].CompletionTokens != 2 {
		t.Errorf("a systemone answer counts its tokens as input and output, got %+v", stats[1])
	}

	// Each kind of group answers only its own kind of call.
	if w := post("classifiers", "chat/completions", `{"model": "x"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "holds System One models") {
		t.Errorf("a chat completion to a System One group should be refused, got %d: %s", w.Code, w.Body.String())
	}
	if w := post("writers", "systemone", `{"model": "x"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "holds language models") {
		t.Errorf("a systemone call to a language group should be refused, got %d: %s", w.Code, w.Body.String())
	}
	if okCalls != 1 {
		t.Errorf("a refused call must not reach a provider, got %d calls", okCalls)
	}
}
