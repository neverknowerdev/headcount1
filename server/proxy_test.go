package server_test

import (
	"context"
	"fmt"
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

func TestProxyChatCompletionsStreamUsage(t *testing.T) {
	// Setup DB
	database, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	database.AutoMigrate(&db.Company{}, &db.LLMProvider{}, &db.Agent{}, &db.ProxyRequestLog{})

	comp := db.Company{Name: "Test"}
	database.Create(&comp)

	provider := db.LLMProvider{Name: "Test Provider", BaseUrl: "http://example.com", ApiKeyEncrypted: sealKey("test-key")}
	database.Create(&provider)

	agent := db.Agent{CompanyID: comp.ID, Name: "Test Agent", ProviderID: &provider.ID}
	database.Create(&agent)

	// Mock target server
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\": [{\"delta\": {\"content\": \"hello\"}}]}\n"))
		w.Write([]byte("data: {\"usage\": {\"prompt_tokens\": 10, \"completion_tokens\": 5, \"total_tokens\": 15}}\n"))
		w.Write([]byte("data: [DONE]\n"))
	}))
	defer targetServer.Close()

	provider.BaseUrl = targetServer.URL
	database.Save(&provider)

	// Test Gateway
	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	reqBody := `{"model": "gpt-4", "stream": true}`
	req := httptest.NewRequest("POST", "/proxy/agent/1/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("agent_id", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()

	// Execute Request
	r.ServeHTTP(w, req)

	// Check response status
	if w.Code != 200 {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	// Wait for async log creation
	time.Sleep(100 * time.Millisecond)

	// Verify Log in DB
	var log db.ProxyRequestLog
	if err := database.First(&log).Error; err != nil {
		t.Fatalf("expected proxy request log to be created, got err: %v", err)
	}

	if log.PromptTokens != 10 || log.CompletionTokens != 5 || log.TotalTokens != 15 {
		t.Errorf("expected 10/5/15 tokens, got %d/%d/%d", log.PromptTokens, log.CompletionTokens, log.TotalTokens)
	}
}

// TestProxyChatCompletionsForProviderPath exercises the path-addressed
// provider endpoint (/proxy/provider/{id}/...) used by hindsight-api, which
// can only configure a base URL + bearer token. The request body must reach
// the provider byte-for-byte (temperature etc. preserved) with the client's
// placeholder Authorization replaced by the provider's real key.
func TestProxyChatCompletionsForProviderPath(t *testing.T) {
	database, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	database.AutoMigrate(&db.LLMProvider{}, &db.ProxyRequestLog{})

	var gotBody, gotAuth string
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer targetServer.Close()

	provider := db.LLMProvider{Name: "P", BaseUrl: targetServer.URL, ApiKeyEncrypted: sealKey("real-key")}
	database.Create(&provider)

	gw := integration.NewLLMGateway(database)
	r := chi.NewRouter()
	gw.Mount(r)

	reqBody := `{"model":"m1","temperature":0.9,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", fmt.Sprintf("/proxy/provider/%d/v1/chat/completions", provider.ID), strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer internal")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotBody != reqBody {
		t.Errorf("body was not passed through verbatim:\n want %s\n got  %s", reqBody, gotBody)
	}
	if gotAuth != "Bearer real-key" {
		t.Errorf("expected provider key to replace the placeholder, got %q", gotAuth)
	}
}
