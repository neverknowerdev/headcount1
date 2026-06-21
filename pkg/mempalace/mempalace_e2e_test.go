package mempalace_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/mempalace"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// requireMempalace skips the test if mempalace-mcp is not in PATH.
func requireMempalace(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("mempalace-mcp"); err != nil {
		t.Skip("mempalace-mcp not found in PATH; skipping MemPalace e2e tests")
	}
}

// newTestClient creates a MemPalace client backed by a fresh temp palace.
// Uses sqlite_exact for speed — no chromadb daemon needed.
func newTestClient(t *testing.T, palace string) *mempalace.Client {
	t.Helper()
	c, err := mempalace.NewClientWithPalace(context.Background(), palace, "sqlite_exact")
	require.NoError(t, err)
	require.NotNil(t, c, "mempalace-mcp must be installed")
	t.Cleanup(func() { c.Close() })
	return c
}

// ── Client-level tests ────────────────────────────────────────────────────────

// TestMempalaceDiaryPersistenceAcrossClients verifies that a diary entry
// written by one client is readable by a second client pointed at the same
// palace after the first client is closed.
func TestMempalaceDiaryPersistenceAcrossClients(t *testing.T) {
	requireMempalace(t)
	palace := filepath.Join(t.TempDir(), "palace")
	unique := fmt.Sprintf("UNIQUE_ENTRY_%d", time.Now().UnixNano())

	// Write with client 1.
	c1 := newTestClient(t, palace)
	err := c1.DiaryWrite(context.Background(), "programmer", unique, "acme", "test")
	require.NoError(t, err)
	c1.Close()

	// Read with client 2 (fresh process pointing at the same palace).
	c2 := newTestClient(t, palace)
	diary, err := c2.DiaryRead(context.Background(), "programmer", "", 10)
	require.NoError(t, err)

	assert.True(t, strings.Contains(diary, unique),
		"diary entry written by client1 should be readable by client2; got: %s", diary)
	assert.True(t, mempalace.HasResults(diary), "HasResults should be true when entries exist")
}

// TestMempalaceDrawerSearchRetrieval verifies that content added via AddDrawer
// is returned by a subsequent Search with a semantically matching query.
func TestMempalaceDrawerSearchRetrieval(t *testing.T) {
	requireMempalace(t)
	palace := filepath.Join(t.TempDir(), "palace")
	unique := fmt.Sprintf("BEARER_JWT_SENTINEL_%d", time.Now().UnixNano())

	c := newTestClient(t, palace)
	err := c.AddDrawer(context.Background(), "acme", "api-design",
		"REST endpoints use /v1 prefix. Auth via "+unique+". Rate limit: 100 req/min per IP.")
	require.NoError(t, err)

	results, err := c.Search(context.Background(), "JWT authentication bearer token", "acme", 5)
	require.NoError(t, err)

	assert.True(t, strings.Contains(results, unique),
		"search should return the drawer containing the unique sentinel; got: %s", results)
	assert.True(t, mempalace.HasResults(results))
}

// TestMempalaceCrossAgentDiaryIsolation verifies that diary entries are stored
// per-agent and do not bleed between agents when reading with filtered names.
func TestMempalaceCrossAgentDiaryIsolation(t *testing.T) {
	requireMempalace(t)
	palace := filepath.Join(t.TempDir(), "palace")
	c := newTestClient(t, palace)
	ctx := context.Background()

	ceoEntry := fmt.Sprintf("CEO_DECISION_%d", time.Now().UnixNano())
	progEntry := fmt.Sprintf("PROG_PATTERN_%d", time.Now().UnixNano())

	require.NoError(t, c.DiaryWrite(ctx, "ceo", ceoEntry, "acme", "strategy"))
	require.NoError(t, c.DiaryWrite(ctx, "programmer", progEntry, "acme", "implementation"))

	ceoDiary, err := c.DiaryRead(ctx, "ceo", "acme", 10)
	require.NoError(t, err)
	assert.True(t, strings.Contains(ceoDiary, ceoEntry), "ceo diary should contain ceo entry")
	assert.False(t, strings.Contains(ceoDiary, progEntry), "ceo diary should NOT contain programmer entry")

	progDiary, err := c.DiaryRead(ctx, "programmer", "acme", 10)
	require.NoError(t, err)
	assert.True(t, strings.Contains(progDiary, progEntry), "programmer diary should contain programmer entry")
	assert.False(t, strings.Contains(progDiary, ceoEntry), "programmer diary should NOT contain ceo entry")
}

// TestMempalaceWingScopedSearch verifies that search results are confined to
// the requested wing and do not include drawers from other wings.
func TestMempalaceWingScopedSearch(t *testing.T) {
	requireMempalace(t)
	palace := filepath.Join(t.TempDir(), "palace")
	c := newTestClient(t, palace)
	ctx := context.Background()

	sentinelA := fmt.Sprintf("WING_A_SENTINEL_%d", time.Now().UnixNano())
	sentinelB := fmt.Sprintf("WING_B_SENTINEL_%d", time.Now().UnixNano())

	require.NoError(t, c.AddDrawer(ctx, "company-a", "auth", "Authentication system: "+sentinelA))
	require.NoError(t, c.AddDrawer(ctx, "company-b", "auth", "Authentication system: "+sentinelB))

	resultsA, err := c.Search(ctx, "Authentication system", "company-a", 5)
	require.NoError(t, err)
	assert.True(t, strings.Contains(resultsA, sentinelA), "company-a search should return company-a content")
	assert.False(t, strings.Contains(resultsA, sentinelB), "company-a search should NOT return company-b content")

	resultsB, err := c.Search(ctx, "Authentication system", "company-b", 5)
	require.NoError(t, err)
	assert.True(t, strings.Contains(resultsB, sentinelB), "company-b search should return company-b content")
	assert.False(t, strings.Contains(resultsB, sentinelA), "company-b search should NOT return company-a content")
}

// TestMempalaceHasResultsHelper verifies the HasResults helper correctly
// classifies empty vs non-empty palace responses.
func TestMempalaceHasResultsHelper(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected bool
	}{
		{"empty results array", `{"results": []}`, false},
		{"empty entries array", `{"entries": []}`, false},
		{"non-empty results", `{"results": [{"id": "x"}]}`, true},
		{"non-empty entries", `{"entries": [{"date": "2026"}]}`, true},
		{"non-json non-empty", "some text", true},
		{"empty string", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, mempalace.HasResults(tc.input))
		})
	}
}

// ── Engine-level integration tests ───────────────────────────────────────────

// setupEngineDB creates an in-process SQLite database with all required tables.
func setupEngineDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := gorm.Open(
		sqlite.Open(dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(4)
	require.NoError(t, database.AutoMigrate(
		&db.Company{},
		&db.Project{},
		&db.Sprint{},
		&db.LLMProvider{},
		&db.Agent{},
		&db.Skill{},
		&db.Task{},
		&db.Comment{},
		&db.Attachment{},
		&db.Run{},
		&db.ActivityLog{},
		&db.ProxyRequestLog{},
		&db.MCPServer{},
		&db.AgentMCPServer{},
	))
	return database
}

// seedTask creates the minimal DB records needed to run a task through the engine.
func seedTask(t *testing.T, database *gorm.DB, providerURL, taskTitle string) db.Task {
	t.Helper()
	q := db.New(database)
	ctx := context.Background()

	company, err := q.CreateCompany(ctx, "e2e-co")
	require.NoError(t, err)

	var sprint db.Sprint
	require.NoError(t, database.Create(&db.Sprint{CompanyID: company.ID, Name: "S1"}).Error)
	require.NoError(t, database.First(&sprint).Error)

	var provider db.LLMProvider
	require.NoError(t, database.Create(&db.LLMProvider{
		Name: "mock", BaseUrl: providerURL, ApiKey: "k", ProviderType: "openai", DefaultModel: "m",
	}).Error)
	require.NoError(t, database.First(&provider).Error)

	pID := provider.ID
	var agent db.Agent
	require.NoError(t, database.Create(&db.Agent{
		CompanyID:    company.ID,
		Name:         "Test Agent",
		SystemPrompt: "You are a test agent.",
		ProviderID:   &pID,
		Model:        "m",
	}).Error)
	require.NoError(t, database.First(&agent).Error)

	aID := agent.ID
	var task db.Task
	require.NoError(t, database.Create(&db.Task{
		CompanyID:       company.ID,
		SprintID:        sprint.ID,
		AgentID:         &aID,
		Title:           taskTitle,
		TaskType:        db.TaskTypeImplement,
		Status:          "to-do",
		AgentConfigName: "Programmer",
	}).Error)
	require.NoError(t, database.First(&task).Error)
	return task
}

// waitForRunCreatedE2E polls until a run row exists for the task.
func waitForRunCreatedE2E(t *testing.T, database *gorm.DB, taskID int32, timeout time.Duration) int32 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var runID sql.NullInt64
		if err := database.Raw("SELECT id FROM runs WHERE task_id = ? ORDER BY id DESC LIMIT 1", taskID).Scan(&runID).Error; err == nil && runID.Valid {
			return int32(runID.Int64)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task %d did not get a run within %v", taskID, timeout)
	return 0
}

// waitForRunDoneE2E polls until the run reaches a terminal status.
func waitForRunDoneE2E(t *testing.T, database *gorm.DB, runID int32, timeout time.Duration) db.Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var run db.Run
		if err := database.First(&run, runID).Error; err == nil {
			if run.Status == "completed" || run.Status == "failed" || run.Status == "canceled" {
				return run
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("run %d did not finish within %v", runID, timeout)
	return db.Run{}
}

// statusThenTextLLM returns a mock LLM that calls update_task_status on turn 1
// and returns a text response on turn 2.
func statusThenTextLLM() http.Handler {
	var n atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1) == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"id": "c1", "model": "m",
				"choices": []any{map[string]any{
					"index": 0,
					"message": map[string]any{
						"role": "assistant", "content": "",
						"tool_calls": []any{map[string]any{
							"id": "tc1", "type": "function",
							"function": map[string]any{
								"name":      "update_task_status",
								"arguments": `{"status":"in-review"}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c2", "model": "m",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "OAuth2 login implemented."},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 8, "total_tokens": 28},
		})
	})
}

// TestMempalacePreRunContextInjection verifies that the NativeEngine injects
// MemPalace context into the first user message when the palace contains
// a diary entry relevant to the current task.
func TestMempalacePreRunContextInjection(t *testing.T) {
	requireMempalace(t)

	palace := filepath.Join(t.TempDir(), "palace")
	t.Setenv("MEMPALACE_PALACE", palace)
	t.Setenv("MEMPALACE_BACKEND", "sqlite_exact")

	// Seed the palace with a unique diary entry for the Programmer agent.
	sentinel := fmt.Sprintf("PRERUN_SENTINEL_%d", time.Now().UnixNano())
	seedCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seedC, err := mempalace.NewClientWithPalace(seedCtx, palace, "sqlite_exact")
	require.NoError(t, err)
	require.NotNil(t, seedC)
	require.NoError(t, seedC.DiaryWrite(seedCtx, "programmer", sentinel, "e2e-co", "test"))
	seedC.Close()

	// Capture the body of every LLM request.
	var firstBody atomic.Value
	var callCount atomic.Int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if callCount.Add(1) == 1 {
			firstBody.Store(body)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "model": "m",
			"choices": []any{map[string]any{
				"index": 0,
				"message": map[string]any{
					"role": "assistant", "content": "",
					"tool_calls": []any{map[string]any{
						"id": "tc1", "type": "function",
						"function": map[string]any{"name": "update_task_status", "arguments": `{"status":"in-review"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	t.Cleanup(mockSrv.Close)

	database := setupEngineDB(t)
	task := seedTask(t, database, mockSrv.URL, "implement oauth2 login")
	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	runID := waitForRunCreatedE2E(t, database, task.ID, 10*time.Second)
	waitForRunDoneE2E(t, database, runID, 30*time.Second)

	rawBody := firstBody.Load()
	require.NotNil(t, rawBody, "LLM was never called")

	var reqBody struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(rawBody.([]byte), &reqBody))

	var userContent string
	for _, m := range reqBody.Messages {
		if m.Role == "user" {
			userContent = m.Content
			break
		}
	}
	require.NotEmpty(t, userContent, "no user message found in LLM request")

	assert.Contains(t, userContent, sentinel,
		"first user message should contain the pre-seeded diary sentinel")
	assert.Contains(t, userContent, "Memory from past runs",
		"user message should have the 'Memory from past runs' header")
}

// TestMempalacePostRunDiaryWrite verifies that after a successful agent run the
// NativeEngine automatically writes a diary entry to MemPalace for the agent.
func TestMempalacePostRunDiaryWrite(t *testing.T) {
	requireMempalace(t)

	palace := filepath.Join(t.TempDir(), "palace")
	t.Setenv("MEMPALACE_PALACE", palace)
	t.Setenv("MEMPALACE_BACKEND", "sqlite_exact")

	mockSrv := httptest.NewServer(statusThenTextLLM())
	t.Cleanup(mockSrv.Close)

	database := setupEngineDB(t)
	task := seedTask(t, database, mockSrv.URL, "implement oauth2 login")
	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	runID := waitForRunCreatedE2E(t, database, task.ID, 10*time.Second)
	waitForRunDoneE2E(t, database, runID, 30*time.Second)

	// Poll MemPalace until the async diary goroutine has finished writing.
	var diary string
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := mempalace.NewClientWithPalace(ctx, palace, "sqlite_exact")
		if err != nil || c == nil {
			return false
		}
		defer c.Close()
		diary, _ = c.DiaryRead(ctx, "programmer", "", 10)
		return mempalace.HasResults(diary) && strings.Contains(strings.ToLower(diary), "oauth2")
	}, 15*time.Second, 500*time.Millisecond, "post-run diary entry should appear in MemPalace")

	assert.Contains(t, strings.ToLower(diary), "oauth2",
		"diary should mention the task title")
	assert.Contains(t, diary, "task-",
		"diary should use the task-<id> AAAK format")
}

// TestMempalaceMemoryInfluencesSubsequentRun verifies the complete memory loop:
// a diary entry from a simulated prior run is present in the user message of
// the next run via pre-run context injection.
func TestMempalaceMemoryInfluencesSubsequentRun(t *testing.T) {
	requireMempalace(t)

	palace := filepath.Join(t.TempDir(), "palace")
	t.Setenv("MEMPALACE_PALACE", palace)
	t.Setenv("MEMPALACE_BACKEND", "sqlite_exact")

	// Seed a diary entry that simulates a completed prior run.
	sentinel := fmt.Sprintf("PRIOR_RUN_OAUTH_SENTINEL_%d", time.Now().UnixNano())
	seedCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seedC, err := mempalace.NewClientWithPalace(seedCtx, palace, "sqlite_exact")
	require.NoError(t, err)
	require.NotNil(t, seedC)
	require.NoError(t, seedC.DiaryWrite(seedCtx, "programmer", sentinel, "e2e-co", "implementation"))
	seedC.Close()

	// Capture the user message from the first LLM call.
	var capturedUserMsg atomic.Value
	var callCount atomic.Int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := callCount.Add(1)
		if n == 1 {
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			if json.Unmarshal(body, &req) == nil {
				for _, m := range req.Messages {
					if m.Role == "user" {
						capturedUserMsg.Store(m.Content)
						break
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"id": "c1", "model": "m",
				"choices": []any{map[string]any{
					"index": 0,
					"message": map[string]any{
						"role": "assistant", "content": "",
						"tool_calls": []any{map[string]any{
							"id": "tc1", "type": "function",
							"function": map[string]any{"name": "update_task_status", "arguments": `{"status":"in-review"}`},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c2", "model": "m",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "done"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
		})
	}))
	t.Cleanup(mockSrv.Close)

	database := setupEngineDB(t)
	task := seedTask(t, database, mockSrv.URL, "add oauth2 refresh token support")
	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	runID := waitForRunCreatedE2E(t, database, task.ID, 10*time.Second)
	waitForRunDoneE2E(t, database, runID, 30*time.Second)

	userMsg, ok := capturedUserMsg.Load().(string)
	require.True(t, ok, "no user message was captured from the LLM request")
	assert.Contains(t, userMsg, sentinel,
		"the current run's user message should contain the prior run's diary sentinel")
}
