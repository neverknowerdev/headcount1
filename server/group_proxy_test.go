package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/integration"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

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
	if err := database.AutoMigrate(
		&db.Company{}, &db.LLMProvider{}, &db.Agent{}, &db.ProxyRequestLog{},
		&db.ModelGroup{}, &db.ModelGroupMember{}, &db.ModelRequestStat{},
		&db.Sprint{}, &db.Task{}, &db.Run{}, &db.RunLogEntry{},
	); err != nil {
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

	p1 := db.LLMProvider{Name: "Limited", BaseUrl: failServer.URL, ApiKey: "k1"}
	p2 := db.LLMProvider{Name: "Backup", BaseUrl: okServer.URL, ApiKey: "k2"}
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

	pPaid := db.LLMProvider{Name: "Paid", BaseUrl: paidServer.URL, ApiKey: "k"}
	pFree := db.LLMProvider{Name: "Free", BaseUrl: freeServer.URL, ApiKey: "k"}
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

	p := db.LLMProvider{Name: "Down", BaseUrl: srv.URL, ApiKey: "k"}
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

// TestAgentProxyUsesModelGroup: an agent bound to a model group must be
// served by the group router on the agent proxy endpoint, including model
// rewriting and failover.
func TestAgentProxyUsesModelGroup(t *testing.T) {
	database := setupGroupTestDB(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		json.Unmarshal(body, &payload)
		if payload["model"] != "grp-model" {
			t.Errorf("expected group member model grp-model, got %v", payload["model"])
		}
		w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}], "usage": {"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}}`))
	}))
	defer srv.Close()

	comp := db.Company{Name: "T", ShortName: "t"}
	database.Create(&comp)
	p := db.LLMProvider{Name: "P", BaseUrl: srv.URL, ApiKey: "k"}
	database.Create(&p)
	group := db.ModelGroup{Name: "Agent Group", Slug: "agent-group"}
	database.Create(&group)
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p.ID, Model: "grp-model", IsFree: true})
	agent := db.Agent{CompanyID: comp.ID, Name: "A", ModelGroupID: &group.ID}
	database.Create(&agent)

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	req := httptest.NewRequest("POST", "/proxy/agent/1/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("agent_id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	time.Sleep(100 * time.Millisecond)
	var stat db.ModelRequestStat
	if err := database.First(&stat).Error; err != nil {
		t.Fatalf("expected a group stat row: %v", err)
	}
	if !stat.Success || stat.Model != "grp-model" {
		t.Errorf("unexpected stat row: %+v", stat)
	}
}

// TestGroupProxySwitchesOnlyLogging: with X-Proxy-Log-Mode: switches-only
// (set by the native engine), the router writes model_switch entries into
// the run log but never request/response entries — the engine's own agent
// loop logs those.
func TestGroupProxySwitchesOnlyLogging(t *testing.T) {
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
	p1 := db.LLMProvider{Name: "Bad", BaseUrl: failServer.URL, ApiKey: "k"}
	p2 := db.LLMProvider{Name: "Good", BaseUrl: okServer.URL, ApiKey: "k"}
	database.Create(&p1)
	database.Create(&p2)
	group := db.ModelGroup{Name: "Run Group", Slug: "run-group"}
	database.Create(&group)
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p1.ID, Model: "m1", IsFree: true, Priority: 0})
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: p2.ID, Model: "m2", IsFree: true, Priority: 1})
	agent := db.Agent{CompanyID: comp.ID, Name: "A", ModelGroupID: &group.ID}
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
	req.Header.Set("X-Proxy-Log-Mode", "switches-only")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("group_key", "run-group")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	time.Sleep(200 * time.Millisecond)

	var entries []db.RunLogEntry
	database.Where("run_id = ?", run.ID).Order("seq asc").Find(&entries)
	var switches, other int
	for _, e := range entries {
		switch e.Type {
		case "model_switch":
			switches++
		case "request", "response":
			other++
		}
	}
	if switches != 1 {
		t.Errorf("expected 1 model_switch entry, got %d (%d total entries)", switches, len(entries))
	}
	if other != 0 {
		t.Errorf("switches-only mode must not log request/response entries, got %d", other)
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

	p := db.LLMProvider{Name: "AnyProvider", BaseUrl: srv.URL, ApiKey: "k", SupportedModels: "model-a,model-b"}
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

	p := db.LLMProvider{Name: "AnyProvider", BaseUrl: srv.URL, ApiKey: "k", SupportedModels: "model-a,model-b,model-c"}
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
