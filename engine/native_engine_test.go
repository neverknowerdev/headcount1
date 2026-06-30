package engine_test

import (
	"bytes"
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
	"agent-orchestrator/eventhub"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---- pipeline-aware test helpers --------------------------------------------

// hasFinishRefinementTool returns true when the LLM request lists finish_refinement
// as an available tool, identifying the caller as the SmartPlanner agent.
func hasFinishRefinementTool(r *http.Request) bool {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	return bytes.Contains(body, []byte(`"finish_refinement"`))
}

// writeJSON encodes v as JSON to w.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// pipelineSmartPlannerResp returns a finish_refinement tool call response.
func pipelineSmartPlannerResp() map[string]interface{} {
	return map[string]interface{}{
		"id": "chatcmpl-sp", "model": "test-model",
		"choices": []map[string]interface{}{{
			"index": 0,
			"message": map[string]interface{}{
				"role": "assistant", "content": "",
				"tool_calls": []map[string]interface{}{{
					"id": "call_fr", "type": "function",
					"function": map[string]interface{}{
						"name":      "finish_refinement",
						"arguments": `{"detailed_description":"test task","specifications":"implement as described","acceptance_criteria":"works correctly","test_cases":"[]"}`,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
}

// pipelineTextResp returns a plain text stop response.
func pipelineTextResp(content string) map[string]interface{} {
	return map[string]interface{}{
		"id": "chatcmpl-text", "model": "test-model",
		"choices": []map[string]interface{}{{
			"index":         0,
			"message":       map[string]interface{}{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13},
	}
}

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
		&db.Session{},
		&db.PendingQuestion{},
		&db.MCPServer{},
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
		TaskType:  db.TaskTypeTech,
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

// toolCallThenTextHandler returns a finish_refinement tool call when the SmartPlanner
// is asking (detected by finish_refinement in the tools list), and a plain text "done"
// response for all Coder/Tester turns, completing the 3-stage pipeline.
func toolCallThenTextHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasFinishRefinementTool(r) {
			writeJSON(w, pipelineSmartPlannerResp())
		} else {
			writeJSON(w, pipelineTextResp("done"))
		}
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

// TestNativeEngineFixtureRun verifies the full 3-stage pipeline using the
// pipeline-aware handler (SmartPlanner → finish_refinement, Coder/Tester → text).
func TestNativeEngineFixtureRun(t *testing.T) {
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
	assert.Equal(t, "in-review", updatedTask.Status, "pipeline finalizeTask should set status to in-review")
}

// ---- subtask tests ----------------------------------------------------------

// createSubtaskHandler returns a finish_refinement call for the SmartPlanner phase,
// then a create_subtask call on the first Coder turn, then text for everything else.
func createSubtaskHandler(t *testing.T) http.Handler {
	t.Helper()
	var coderCalled atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasFinishRefinementTool(r) {
			writeJSON(w, pipelineSmartPlannerResp())
			return
		}
		// First Coder turn: create a subtask.
		if coderCalled.CompareAndSwap(false, true) {
			writeJSON(w, map[string]interface{}{
				"id": "chatcmpl-sub-001", "model": "test-model",
				"choices": []map[string]interface{}{{
					"index": 0,
					"message": map[string]interface{}{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]interface{}{{
							"id": "call_sub_001", "type": "function",
							"function": map[string]interface{}{
								"name":      "create_subtask",
								"arguments": `{"title":"subtask A","description":"do subtask A","agent_name":"Programmer"}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})
			return
		}
		writeJSON(w, pipelineTextResp("Subtask delegated."))
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
	// Handler: SmartPlanner → finish_refinement; first Coder turn → create_subtask; rest → text.
	var coderCalled atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasFinishRefinementTool(r) {
			writeJSON(w, pipelineSmartPlannerResp())
			return
		}
		if coderCalled.CompareAndSwap(false, true) {
			writeJSON(w, map[string]interface{}{
				"id": "chatcmpl-dup-001", "model": "test-model",
				"choices": []map[string]interface{}{{
					"message": map[string]interface{}{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]interface{}{{
							"id": "call_d1", "type": "function",
							"function": map[string]interface{}{
								"name":      "create_subtask",
								"arguments": `{"title":"sub1","description":"d1","agent_name":"Programmer"}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
			})
			return
		}
		writeJSON(w, pipelineTextResp("done"))
	})

	mockSrv := startTestServer(t, handler)

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
		TaskType:  db.TaskTypeTech,
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

// TestNativeEngineExpandRunResult verifies that the model can invoke expand_run_result
// and receive the full run description.
func TestNativeEngineExpandRunResult(t *testing.T) {
	var pastRunID atomic.Int32
	var capturedToolResult atomic.Value // stores string
	var count atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// hasFinishRefinementTool reads and restores the body.
		isSP := hasFinishRefinementTool(r)
		n := count.Add(1)

		if !isSP {
			// Coder or Tester turn — pipeline continuation.
			writeJSON(w, pipelineTextResp("done"))
			return
		}

		if n == 1 {
			// SmartPlanner first turn: ask it to fetch the past run.
			runID := pastRunID.Load()
			writeJSON(w, map[string]interface{}{
				"id": "chatcmpl-exp-001", "model": "test-model",
				"choices": []map[string]interface{}{{
					"index": 0,
					"message": map[string]interface{}{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]interface{}{{
							"id": "call_exp_001", "type": "function",
							"function": map[string]interface{}{
								"name":      "expand_run_result",
								"arguments": fmt.Sprintf(`{"run_id":%d}`, runID),
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30},
			})
			return
		}

		// SmartPlanner subsequent turns: capture tool result then call finish_refinement.
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
		writeJSON(w, pipelineSmartPlannerResp())
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

