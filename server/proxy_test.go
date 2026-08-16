package server_test

import (
	"agent-orchestrator/db/migrations"
	"context"
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
	migrations.ApplyGORM(database, "sqlite", "test")

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
