package engine_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/eventhub"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---- test helpers -----------------------------------------------------------

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Use a file-based SQLite DB with WAL mode so concurrent goroutines
	// (test poller + engine goroutine) can each hold their own connection.
	// ":memory:" with MaxOpenConns(1) causes starvation: the polling loop
	// blocks the engine goroutine from ever acquiring the connection.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"), &gorm.Config{})
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
	))
	return database
}

// waitForSubtask polls until at least one subtask of parentTaskID exists in the DB.
func waitForSubtask(t *testing.T, database *gorm.DB, parentTaskID int32, timeout time.Duration) db.Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var tasks []db.Task
		if err := database.Where("parent_id = ?", parentTaskID).Find(&tasks).Error; err == nil && len(tasks) > 0 {
			return tasks[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no subtask of task %d appeared within %v", parentTaskID, timeout)
	return db.Task{}
}

func seedTestData(t *testing.T, database *gorm.DB, mockProviderURL string) (task db.Task) {
	t.Helper()
	q := db.New(database)
	ctx := context.Background()

	company, err := q.CreateCompany(ctx, "Test Co")
	require.NoError(t, err)

	var sprint db.Sprint
	require.NoError(t, database.Create(&db.Sprint{CompanyID: company.ID, Name: "Sprint 1"}).Error)
	require.NoError(t, database.First(&sprint, "company_id = ?", company.ID).Error)

	var provider db.LLMProvider
	require.NoError(t, database.Create(&db.LLMProvider{
		Name:         "mock-provider",
		BaseUrl:      mockProviderURL,
		ApiKey:       "test-key",
		ProviderType: "openai",
		DefaultModel: "test-model",
	}).Error)
	require.NoError(t, database.First(&provider, "name = ?", "mock-provider").Error)

	providerID := provider.ID
	var agent db.Agent
	require.NoError(t, database.Create(&db.Agent{
		CompanyID:    company.ID,
		Name:         "Test Agent",
		SystemPrompt: "You are a helpful agent.",
		ProviderID:   &providerID,
		Model:        "test-model",
	}).Error)
	require.NoError(t, database.First(&agent, "company_id = ?", company.ID).Error)

	agentID := agent.ID
	require.NoError(t, database.Create(&db.Task{
		CompanyID: company.ID,
		SprintID:  sprint.ID,
		AgentID:   &agentID,
		Title:     "Test Task",
		TaskType:  db.TaskTypeImplement,
		Status:    "to-do",
	}).Error)
	require.NoError(t, database.First(&task, "company_id = ?", company.ID).Error)
	return
}

// startTestServer starts an httptest.Server and registers cleanup.
func startTestServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// waitForRunDone polls the DB until the given run reaches a terminal status or
// the timeout expires. Returns the final Run.
func waitForRunDone(t *testing.T, q *db.Queries, runID int32, timeout time.Duration) db.Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run, err := q.GetRun(context.Background(), runID)
		if err == nil && (run.Status == "completed" || run.Status == "failed" || run.Status == "canceled") {
			return run
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("run %d did not finish within %v", runID, timeout)
	return db.Run{}
}

// waitForRunCreated waits until a Run record exists for the task. It polls
// the runs table directly instead of task.run_id, because the engine may
// complete (and clear run_id via UnlockTaskRun) before the first poll.
func waitForRunCreated(t *testing.T, database *gorm.DB, taskID int32, timeout time.Duration) int32 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var runID sql.NullInt64
		if err := database.Raw("SELECT id FROM runs WHERE task_id = ? ORDER BY id DESC LIMIT 1", taskID).Scan(&runID).Error; err == nil && runID.Valid {
			return int32(runID.Int64)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task %d did not get a run created within %v", taskID, timeout)
	return 0
}

// ---- mock LLM handler -------------------------------------------------------

// toolCallThenTextHandler returns a finish_task tool call on the first
// chat-completions request, then a plain text response on subsequent requests.
func toolCallThenTextHandler(t *testing.T) http.Handler {
	t.Helper()
	var count atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := count.Add(1)
		if n == 1 {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "chatcmpl-001", "model": "test-model",
				"choices": []map[string]interface{}{{
					"index": 0,
					"message": map[string]interface{}{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]interface{}{{
							"id": "call_001", "type": "function",
							"function": map[string]interface{}{
								"name":      "finish_task",
								"arguments": `{"task_status":"in-review","finish_status":"Task completed and ready for review"}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 50, "completion_tokens": 20, "total_tokens": 70},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "chatcmpl-002", "model": "test-model",
			"choices": []map[string]interface{}{{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": "Task completed and marked in-review."},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 80, "completion_tokens": 12, "total_tokens": 92},
		})
	})
}

// ---- tests ------------------------------------------------------------------

// TestNativeEngineProcessTask runs a full end-to-end ProcessTask with a mock
// LLM that issues a tool call followed by a text response.
func TestNativeEngineProcessTask(t *testing.T) {
	mockSrv := startTestServer(t, toolCallThenTextHandler(t))
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)

	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	q := db.New(database)
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	run := waitForRunDone(t, q, runID, 30*time.Second)

	assert.Equal(t, "completed", run.Status)

	updatedTask, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, "in-review", updatedTask.Status, "agent should have called finish_task")

	comments, err := q.ListCommentsByTask(context.Background(), task.ID)
	require.NoError(t, err)
	agentComments := 0
	for _, c := range comments {
		if c.AuthorType == "agent" {
			agentComments++
		}
	}
	assert.Greater(t, agentComments, 0, "agent comment should have been created")
}

// TestNativeEngineStopRun verifies that StopRun cancels an in-progress run.
func TestNativeEngineStopRun(t *testing.T) {
	var slowCalls atomic.Int32
	// shutdownCh lets the test explicitly unblock the slow handler before the
	// httptest.Server cleanup runs. t.Cleanup(srv.Close) is registered inside
	// startTestServer; registering close(shutdownCh) AFTER that means it
	// executes FIRST (LIFO), so the handler returns before Close waits.
	shutdownCh := make(chan struct{})
	slowHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowCalls.Add(1)
		select {
		case <-r.Context().Done():
		case <-shutdownCh:
		}
		w.WriteHeader(499)
	})
	mockSrv := startTestServer(t, slowHandler)
	t.Cleanup(func() { close(shutdownCh) }) // LIFO: runs before srv.Close
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)

	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	// Wait for the run to be registered and the LLM call to have started.
	q := db.New(database)
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	require.Eventually(t, func() bool { return slowCalls.Load() >= 1 }, 10*time.Second, 50*time.Millisecond)

	eng.StopRun(context.Background(), runID)

	run := waitForRunDone(t, q, runID, 15*time.Second)
	assert.Equal(t, "canceled", run.Status)
}

// TestNativeEngineDeduplication ensures that calling ProcessTask twice for the
// same active task does not spawn a second run.
func TestNativeEngineDeduplication(t *testing.T) {
	var callCount atomic.Int32
	// Handler that blocks on first call so the run stays active.
	blockCh := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		<-blockCh
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "chatcmpl-dup", "model": "test-model",
			"choices": []map[string]interface{}{{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": "done"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11},
		})
	})
	mockSrv := startTestServer(t, handler)
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)

	// First call — starts the goroutine.
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))
	waitForRunCreated(t, database, task.ID, 10*time.Second)

	// Second call — should be a no-op because the run is still active.
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))
	time.Sleep(200 * time.Millisecond)

	// Unblock the first (and only) LLM call.
	close(blockCh)

	// Only one LLM call should have been made.
	assert.Equal(t, int32(1), callCount.Load(), "second ProcessTask should not have started a new LLM call")
}

// TestNativeEngineFixtureRun verifies the engine using the pre-recorded fixture
// that encodes a tool_call-then-text interaction.
func TestNativeEngineFixtureRun(t *testing.T) {
	fixturePath := filepath.Join("aicli", "testdata", "fixtures", "tool_call.json")
	ft := aicli.NewFixtureTransport(fixturePath, nil)

	// Wrap the fixture transport in an HTTP handler so the engine can talk to it.
	mockSrv := startTestServer(t, fixtureHandler(ft))

	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)

	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	q := db.New(database)
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	run := waitForRunDone(t, q, runID, 30*time.Second)

	assert.Equal(t, "completed", run.Status)

	updatedTask, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, "in-review", updatedTask.Status, "fixture encodes a finish_task(in-review) call")
}

// ---- subtask tests ----------------------------------------------------------

// createSubtaskHandler returns a mock LLM that first calls create_subtask via the
// paperclip2 MCP server, then acknowledges the result with a text response.
func createSubtaskHandler(t *testing.T) http.Handler {
	t.Helper()
	var count atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := count.Add(1)
		switch n {
		case 1:
			// First turn: call create_subtask via MCP
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "chatcmpl-sub-001", "model": "test-model",
				"choices": []map[string]interface{}{{
					"index": 0,
					"message": map[string]interface{}{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]interface{}{{
							"id": "call_sub_001", "type": "function",
							"function": map[string]interface{}{
								"name":      "call_mcp_tool",
								"arguments": `{"server":"paperclip2","tool":"create_subtask","input":{"title":"subtask A","description":"do subtask A","agent_name":"Programmer"}}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})
		default:
			// Subsequent turns: plain text
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "chatcmpl-sub-002", "model": "test-model",
				"choices": []map[string]interface{}{{
					"index":         0,
					"message":       map[string]interface{}{"role": "assistant", "content": "Subtask delegated."},
					"finish_reason": "stop",
				}},
				"usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 3, "total_tokens": 23},
			})
		}
	})
}

// TestNativeEngineCreateSubtask verifies that when the LLM calls create_subtask
// a child Task is created in the DB with the correct parent_id.
func TestNativeEngineCreateSubtask(t *testing.T) {
	mockSrv := startTestServer(t, createSubtaskHandler(t))
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)

	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	// Wait for the parent run to appear.
	q := db.New(database)
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	waitForRunDone(t, q, runID, 30*time.Second)

	// A subtask should have been created with the correct parent.
	subtask := waitForSubtask(t, database, task.ID, 5*time.Second)
	assert.Equal(t, task.ID, *subtask.ParentID)
	assert.Equal(t, "subtask A", subtask.Title)
	assert.Equal(t, "Programmer", subtask.AgentConfigName)
}

// TestNativeEngineSubtaskBlocksDuplicate verifies that create_subtask returns an
// error when a subtask is already running, preventing a second one from starting.
func TestNativeEngineSubtaskBlocksDuplicate(t *testing.T) {
	// The first call from the LLM will create a subtask; the second call
	// (same turn, different tool_call) should be blocked because the first
	// subtask is already running.
	var callCount atomic.Int32
	blockCh := make(chan struct{})

	// Handler for the parent agent: tries to create two subtasks in one turn.
	parentHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := callCount.Add(1)
		if n == 1 {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "chatcmpl-dup-001", "model": "test-model",
				"choices": []map[string]interface{}{{
					"message": map[string]interface{}{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]interface{}{
							{
								"id": "call_d1", "type": "function",
								"function": map[string]interface{}{
									"name":      "call_mcp_tool",
									"arguments": `{"server":"paperclip2","tool":"create_subtask","input":{"title":"sub1","description":"d1","agent_name":"Programmer"}}`,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
			})
			return
		}
		// Subsequent parent turns: text response.
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "chatcmpl-dup-002", "model": "test-model",
			"choices": []map[string]interface{}{{
				"message":       map[string]interface{}{"role": "assistant", "content": "done"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6},
		})
	})

	// Handler for the subtask agent: blocks until test unblocks it.
	subtaskHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockCh
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "chatcmpl-sub", "model": "test-model",
			"choices": []map[string]interface{}{{
				"message": map[string]interface{}{
					"role": "assistant", "content": "",
					"tool_calls": []map[string]interface{}{{
						"id": "call_ft", "type": "function",
						"function": map[string]interface{}{
							"name":      "finish_task",
							"arguments": `{"task_status":"in-review","finish_status":"done"}`,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	})

	// Both parent and subtask hit the same mock server; route by call count.
	combined := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If the subtask's run is active, route to the subtask handler.
		// For simplicity, route by overall call count: first two parent, rest subtask.
		n := callCount.Load()
		if n <= 1 {
			parentHandler.ServeHTTP(w, r)
		} else if n == 2 {
			parentHandler.ServeHTTP(w, r) // second parent turn (after tool result)
		} else {
			subtaskHandler.ServeHTTP(w, r)
		}
	})
	_ = combined // suppress unused warning

	mockSrv := startTestServer(t, parentHandler)
	t.Cleanup(func() { close(blockCh) })

	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)

	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	// Wait for the parent run to finish.
	q := db.New(database)
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	waitForRunDone(t, q, runID, 15*time.Second)

	// Exactly one subtask should exist.
	subtasks, err := q.ListSubtasksByParent(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Len(t, subtasks, 1, "only one subtask should be created")
}

// TestNativeEngineSubtaskNotifiesParent verifies that when a subtask completes
// the parent task receives a system comment.
func TestNativeEngineSubtaskNotifiesParent(t *testing.T) {
	mockSrv := startTestServer(t, toolCallThenTextHandler(t))
	database := setupTestDB(t)
	q := db.New(database)

	// Seed a parent task and a subtask (parent → subtask relationship).
	parentTask := seedTestData(t, database, mockSrv.URL)
	agentID := *parentTask.AgentID
	parentID := parentTask.ID
	var subtask db.Task
	require.NoError(t, database.Create(&db.Task{
		CompanyID: parentTask.CompanyID,
		SprintID:  parentTask.SprintID,
		AgentID:   &agentID,
		ParentID:  &parentID,
		Title:     "child task",
		TaskType:  db.TaskTypeImplement,
		Status:    "to-do",
	}).Error)
	require.NoError(t, database.First(&subtask, "parent_id = ?", parentTask.ID).Error)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)

	require.NoError(t, eng.ProcessTask(context.Background(), subtask.ID))

	runID := waitForRunCreated(t, database, subtask.ID, 10*time.Second)
	waitForRunDone(t, q, runID, 30*time.Second)

	// The parent task should have received a system comment about the subtask.
	require.Eventually(t, func() bool {
		comments, err := q.ListCommentsByTask(context.Background(), parentTask.ID)
		if err != nil {
			return false
		}
		for _, c := range comments {
			if c.AuthorType == "system" {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "parent task should have received a system comment")
}

// ---- paperclip2 MCP e2e tests -----------------------------------------------

// TestNativeEnginePaperclipMCPCreateSubtask verifies that the model can invoke
// create_subtask via the paperclip2 builtin MCP server using call_mcp_tool.
func TestNativeEnginePaperclipMCPCreateSubtask(t *testing.T) {
	var count atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := count.Add(1)
		if n == 1 {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "chatcmpl-mcp-001", "model": "test-model",
				"choices": []map[string]interface{}{{
					"index": 0,
					"message": map[string]interface{}{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]interface{}{{
							"id": "call_mcp_001", "type": "function",
							"function": map[string]interface{}{
								"name":      "call_mcp_tool",
								"arguments": `{"server":"paperclip2","tool":"create_subtask","input":{"title":"MCP subtask","description":"created via MCP","agent_name":"Programmer"}}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "chatcmpl-mcp-002", "model": "test-model",
			"choices": []map[string]interface{}{{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": "Subtask created via paperclip2 MCP."},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 30, "completion_tokens": 5, "total_tokens": 35},
		})
	})

	mockSrv := startTestServer(t, handler)
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	q := db.New(database)
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	waitForRunDone(t, q, runID, 30*time.Second)

	subtask := waitForSubtask(t, database, task.ID, 5*time.Second)
	assert.Equal(t, task.ID, *subtask.ParentID)
	assert.Equal(t, "MCP subtask", subtask.Title)
	assert.Equal(t, "Programmer", subtask.AgentConfigName)
}

// TestNativeEnginePaperclipMCPExpandRunResult verifies that the model can invoke
// expand_run_result via the paperclip2 builtin MCP server and receive the run description.
func TestNativeEnginePaperclipMCPExpandRunResult(t *testing.T) {
	var pastRunID atomic.Int32
	var capturedToolResult atomic.Value // stores string
	var count atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := count.Add(1)
		if n == 1 {
			runID := pastRunID.Load()
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "chatcmpl-exp-001", "model": "test-model",
				"choices": []map[string]interface{}{{
					"index": 0,
					"message": map[string]interface{}{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]interface{}{{
							"id": "call_exp_001", "type": "function",
							"function": map[string]interface{}{
								"name":      "call_mcp_tool",
								"arguments": fmt.Sprintf(`{"server":"paperclip2","tool":"expand_run_result","input":{"run_id":%d}}`, runID),
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30},
			})
			return
		}
		if n == 2 {
			// Capture the tool result from the request messages.
			bodyBytes, _ := io.ReadAll(r.Body)
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			if json.Unmarshal(bodyBytes, &req) == nil {
				for _, msg := range req.Messages {
					if msg.Role == "tool" {
						capturedToolResult.Store(msg.Content)
					}
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "chatcmpl-exp-002", "model": "test-model",
			"choices": []map[string]interface{}{{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": "I read the run details."},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 30, "completion_tokens": 5, "total_tokens": 35},
		})
	})

	mockSrv := startTestServer(t, handler)
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	// Insert a completed past run with a known ResultDescription.
	q := db.New(database)
	pastRun, err := q.CreateRun(context.Background(), db.Run{
		TaskID:    task.ID,
		AgentID:   *task.AgentID,
		Status:    "running",
		StartedAt: time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, q.UpdateRunResult(context.Background(), pastRun.ID, "Detailed explanation of the past run.", ""))
	require.NoError(t, q.UpdateRunLog(context.Background(), pastRun.ID, "", "completed"))
	pastRunID.Store(pastRun.ID)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	// Wait for the engine's run (not the pre-existing past run) to appear and complete.
	var engineRunID int32
	require.Eventually(t, func() bool {
		var id sql.NullInt64
		if err := database.Raw("SELECT id FROM runs WHERE task_id = ? AND id > ? ORDER BY id DESC LIMIT 1", task.ID, pastRun.ID).Scan(&id).Error; err == nil && id.Valid {
			engineRunID = int32(id.Int64)
			return true
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "engine run should have been created")
	waitForRunDone(t, q, engineRunID, 30*time.Second)

	// Verify the tool result contained the expected run description.
	toolResult, _ := capturedToolResult.Load().(string)
	assert.Contains(t, toolResult, "Detailed explanation of the past run.")
}

// fixtureHandler wraps a FixtureTransport as an HTTP handler.
func fixtureHandler(ft *aicli.FixtureTransport) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := ft.RoundTrip(r)
		if err != nil {
			http.Error(w, fmt.Sprintf("fixture error: %v", err), 500)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
}
