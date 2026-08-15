package engine_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/secrets"

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
		&db.User{},
		&db.Company{},
		&db.Project{},
		&db.Sprint{},
		&db.LLMProvider{},
		&db.Agent{},
		&db.Skill{},
		&db.Task{},
		&db.TaskRelation{},
		&db.Comment{},
		&db.Attachment{},
		&db.Run{},
		&db.RunStatusReport{},
		&db.RunEvent{},
		&db.Artifact{},
		&db.ActivityLog{},
		&db.ProxyRequestLog{},
		&db.ModelGroup{},
		&db.ModelGroupMember{},
		&db.DefaultModelSetting{},
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
	// Seal the provider key under a fixed, unlocked fixture user so the sealed
	// column holds real "enc:u1:" ciphertext the engine can open at point of use.
	const engineTestUserID int32 = 707070
	var engineTestDEK [32]byte
	engineTestDEK[0], engineTestDEK[1] = 0x70, 0x70
	secrets.Default().UnlockUser(engineTestUserID, engineTestDEK, time.Hour)
	sealedKey, err := secrets.Default().EncryptForUser(engineTestUserID, "test-key")
	require.NoError(t, err)
	require.NoError(t, database.Create(&db.LLMProvider{
		Name:            "mock-provider",
		BaseUrl:         mockProviderURL,
		ApiKeyEncrypted: sealedKey,
		ProviderType:    "openai",
		DefaultModel:    "test-model",
	}).Error)
	require.NoError(t, database.First(&provider, "name = ?", "mock-provider").Error)

	providerID := provider.ID
	var agent db.Agent
	require.NoError(t, database.Create(&db.Agent{
		CompanyID:    company.ID,
		Name:         "Test Agent",
		RoleKey:      "CEO",
		ShortName:    "CEO",
		SystemPrompt: "You are a helpful agent.",
		ProviderID:   &providerID,
		Model:        "test-model",
		Subagents:    `["CTO", "CMO", "Designer"]`,
	}).Error)
	for _, definition := range []struct {
		name      string
		subagents string
	}{
		{name: "CTO", subagents: `["Coder", "Debugger", "QA"]`},
		{name: "CMO"},
		{name: "Coder"},
		{name: "Debugger"},
		{name: "QA"},
		{name: "Designer"},
	} {
		require.NoError(t, database.Create(&db.Agent{
			CompanyID:    company.ID,
			Name:         definition.name,
			RoleKey:      definition.name,
			ShortName:    definition.name,
			SystemPrompt: "You are a test agent.",
			ProviderID:   &providerID,
			Model:        "test-model",
			Subagents:    definition.subagents,
		}).Error)
	}
	require.NoError(t, database.First(&agent, "company_id = ?", company.ID).Error)

	agentID := agent.ID
	// Create via Queries so the ref key ("TASK-1" style) is assigned, as in
	// production.
	created, err := q.CreateTask(ctx, db.Task{
		CompanyID: company.ID,
		SprintID:  sprint.ID,
		AgentID:   &agentID,
		Title:     "Test Task",
		TaskType:  db.TaskTypeImplement,
		Status:    "to-do",
	})
	require.NoError(t, err)
	require.NoError(t, database.First(&task, created.ID).Error)
	return
}

// startTestServer starts an httptest.Server and registers cleanup.
func startTestServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func readJSONLEntries(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var entries []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		entries = append(entries, entry)
	}
	return entries
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

	// The run's JSONL log must close with an outcome entry labelling the
	// trajectory: finish_task was called organically with status in-review.
	logData, err := os.ReadFile(run.LogFilePath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	var outcome map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &outcome))
	assert.Equal(t, "outcome", outcome["type"])
	assert.Equal(t, "completed", outcome["status"])
	assert.Equal(t, "finish_task", outcome["end_reason"])
	assert.Equal(t, "in-review", outcome["task_status"])
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

// ---- graceful-drain / auto-update pause+resume tests -------------------------

// waitForRunStatus polls until the given run reaches the given status or the
// timeout expires.
func waitForRunStatus(t *testing.T, q *db.Queries, runID int32, status string, timeout time.Duration) db.Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run, err := q.GetRun(context.Background(), runID)
		if err == nil && run.Status == status {
			return run
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run %d did not reach status %q within %v", runID, status, timeout)
	return db.Run{}
}

// TestNativeEnginePauseAndResume verifies the full graceful-drain lifecycle
// behind the auto-update feature: BeginDrain stops new runs from starting,
// an in-flight run pauses right after its current turn's LLM response
// arrives (its pending tool call is never executed), the paused run's
// conversation is persisted and the task stays locked to it, and a *fresh*
// NativeEngine instance backed by the same DB (simulating the restarted
// process) picks the run back up via ResumeEligibleSessions and completes it —
// including actually running the tool call that was pending at pause time.
//
// The pending tool call is report_status: it's available to every agent
// (including the CEO orchestrator this task routes through) and its side
// effect — writing run.latest_reported_status — is observable in the DB, so we can
// assert it ran on resume and not before.
func TestNativeEnginePauseAndResume(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("E2E_HEADCOUNT1_HOME", tmpHome)

	database := setupTestDB(t)
	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)

	const resumeMarker = "status set after resume"
	var callCount atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch callCount.Add(1) {
		case 1:
			// Flip the engine into drain mode BEFORE this response is served:
			// by the time the agent loop's pause check runs (immediately after
			// receiving this exact response), draining is already true —
			// deterministic, no sleep/race needed.
			eng.BeginDrain()
			json.NewEncoder(w).Encode(toolCallJSON("pr-001", "report_status",
				fmt.Sprintf(`{"status":%q}`, resumeMarker)))
		default:
			json.NewEncoder(w).Encode(toolCallJSON("pr-002", "finish_task",
				`{"task_status":"done","finish_status":"Resumed and completed"}`))
		}
	})
	mockSrv := startTestServer(t, handler)
	task := seedTestData(t, database, mockSrv.URL)
	q := db.New(database)

	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)

	run := waitForRunStatus(t, q, runID, db.RunStatusPaused, 10*time.Second)
	assert.NotZero(t, run.Recovery.CheckpointSequence, "paused run must persist a JSONL checkpoint cursor")
	assert.Equal(t, int32(1), callCount.Load(), "pausing must stop before any follow-up LLM call")
	assert.NotEqual(t, resumeMarker, run.LatestReportedStatus, "the pending tool call must not run before resume")

	// The task must stay locked to the interrupted run so no other run can
	// start on it while it's waiting to resume.
	pausedTask, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.NotNil(t, pausedTask.RunID)
	assert.Equal(t, runID, *pausedTask.RunID)

	// Draining also blocks new runs outright (ProcessTask no-ops).
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(1), callCount.Load(), "ProcessTask must not start a new run while draining")

	// Simulate the restart: a fresh NativeEngine over the same DB. Its
	// in-memory state (cancelFuncs, the drain flag) starts empty, exactly as
	// it would after a real process restart — only the DB carries state across.
	eng2 := engine.NewNativeEngine(database, hub)
	eng2.ResumeEligibleSessions(context.Background())

	finalRun := waitForRunDone(t, q, runID, 15*time.Second)
	assert.Equal(t, "completed", finalRun.Status)
	assert.Zero(t, finalRun.Recovery.CheckpointSequence, "resumed checkpoint should be cleared once consumed")
	assert.Equal(t, int32(2), callCount.Load(), "resume must replay the pending tool call locally, then make exactly one more LLM call")
	assert.Equal(t, resumeMarker, finalRun.LatestReportedStatus, "the tool call pending at pause time must run on resume")

	finishedTask, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, "done", finishedTask.Status)
	// The task unlocks in a deferred step that runs just after the run is
	// marked completed, so poll rather than checking instantly.
	require.Eventually(t, func() bool {
		tk, gErr := q.GetTask(context.Background(), task.ID)
		return gErr == nil && tk.RunID == nil
	}, 5*time.Second, 50*time.Millisecond, "task must be unlocked once the resumed run finishes")
}

// ---- subtask tests ----------------------------------------------------------

// toolCallJSON builds a single-tool-call chat completion response body.
func toolCallJSON(id, name, args string) map[string]interface{} {
	return map[string]interface{}{
		"id": "chatcmpl-" + id, "model": "test-model",
		"choices": []map[string]interface{}{{
			"index": 0,
			"message": map[string]interface{}{
				"role": "assistant", "content": "",
				"tool_calls": []map[string]interface{}{{
					"id": id, "type": "function",
					"function": map[string]interface{}{"name": name, "arguments": args},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
}

func textJSON(id, text string) map[string]interface{} {
	return map[string]interface{}{
		"id": "chatcmpl-" + id, "model": "test-model",
		"choices": []map[string]interface{}{{
			"index":         0,
			"message":       map[string]interface{}{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13},
	}
}

// createSubtaskHandler emulates a full delegation: the parent calls
// create_subtask, the child session finishes with finish_task (a terminal
// tool), and the parent then wraps up. Delegation blocks the parent so the
// global request order is fixed:
//  1. parent -> create_subtask
//  2. child  -> finish_task(done)   (terminal: child session ends)
//  3. parent -> finish_task
func createSubtaskHandler(t *testing.T) http.Handler {
	t.Helper()
	var count atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch count.Add(1) {
		case 1:
			json.NewEncoder(w).Encode(toolCallJSON("del-001", "create_subtask",
				`{"title":"subtask A","description":"do subtask A","agent_name":"CTO"}`))
		case 2:
			json.NewEncoder(w).Encode(toolCallJSON("fin-001", "finish_task",
				`{"task_status":"done","finish_status":"Subtask A implemented"}`))
		default:
			json.NewEncoder(w).Encode(toolCallJSON("fin-002", "finish_task",
				`{"task_status":"in-review","finish_status":"Delegation complete."}`))
		}
	})
}

// TestNativeEngineCreateSubtask verifies that when the LLM calls create_subtask
// a child Task is created, run as a nested session linked to the parent run,
// and its result recorded.
func TestNativeEngineCreateSubtask(t *testing.T) {
	mockSrv := startTestServer(t, createSubtaskHandler(t))
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)

	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	// Wait for the parent run to appear and finish.
	q := db.New(database)
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	waitForRunDone(t, q, runID, 30*time.Second)

	// A subtask should have been created with the correct parent and completed.
	subtask := waitForSubtask(t, database, task.ID, 5*time.Second)
	assert.Equal(t, task.ID, *subtask.ParentID)
	assert.Equal(t, "subtask A", subtask.Title)
	require.NotNil(t, subtask.AgentID)
	var subtaskAgent db.Agent
	require.NoError(t, database.First(&subtaskAgent, "id = ?", *subtask.AgentID).Error)
	assert.Equal(t, "CTO", subtaskAgent.RoleKey)
	assert.Equal(t, "done", subtask.Status, "child session should have finished the subtask")
	// Delegated subtasks carry no raw user input: the orchestrator's
	// instructions land in RefinedDescription, and Description stays empty.
	assert.Empty(t, subtask.Description)
	assert.Equal(t, "do subtask A", subtask.RefinedDescription)

	// The child run must be linked to the parent run (session hierarchy).
	childRunID := waitForRunCreated(t, database, subtask.ID, 5*time.Second)
	childRun, err := q.GetRun(context.Background(), childRunID)
	require.NoError(t, err)
	require.NotNil(t, childRun.ParentRunID)
	assert.Equal(t, runID, *childRun.ParentRunID)
	require.NotNil(t, childRun.RootRunID)
	assert.Equal(t, runID, *childRun.RootRunID)
	assert.Equal(t, "completed", childRun.Status)
	assert.Equal(t, "Subtask A implemented", childRun.ResultDescription)

	// The parent run's log must record the session start and end. Log entries
	// are persisted by fire-and-forget goroutines, so poll instead of reading
	// once — the run can complete a moment before the last entry lands.
	require.Eventually(t, func() bool {
		parentRun, err := q.GetRun(context.Background(), runID)
		if err != nil {
			return false
		}
		return strings.Contains(parentRun.LogEntries, "session_started") &&
			strings.Contains(parentRun.LogEntries, "session_ended")
	}, 5*time.Second, 100*time.Millisecond, "parent run log should contain session_started and session_ended")
}

// TestNativeEngineDelegatedSessionUsesConfiguredAgentSettings verifies that a
// delegated role is resolved to its own database Agent row. The child must use
// that row's model and permissions even though it also receives the built-in
// role config for its prompt and delegation behavior.
func TestNativeEngineDelegatedSessionUsesConfiguredAgentSettings(t *testing.T) {
	type capturedRequest struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}

	var requestsMu sync.Mutex
	var requests []capturedRequest
	var callCount atomic.Int32
	mockSrv := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read mock request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req capturedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode mock request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requestsMu.Lock()
		requests = append(requests, req)
		requestsMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch callCount.Add(1) {
		case 1:
			json.NewEncoder(w).Encode(toolCallJSON("settings-001", "create_subtask",
				`{"title":"configured child","description":"use the CTO settings","agent_name":"CTO"}`))
		case 2:
			json.NewEncoder(w).Encode(toolCallJSON("settings-002", "finish_task",
				`{"task_status":"done","finish_status":"child done"}`))
		default:
			json.NewEncoder(w).Encode(toolCallJSON("settings-003", "finish_task",
				`{"task_status":"done","finish_status":"parent done"}`))
		}
	}))

	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	var parentAgent db.Agent
	require.NoError(t, database.First(&parentAgent, "id = ?", *task.AgentID).Error)
	var parentProvider db.LLMProvider
	require.NoError(t, database.First(&parentProvider, "id = ?", *parentAgent.ProviderID).Error)

	ctoProvider := db.LLMProvider{
		Name:            "cto-provider",
		BaseUrl:         mockSrv.URL,
		ApiKeyEncrypted: parentProvider.ApiKeyEncrypted,
		ProviderType:    "openai",
		DefaultModel:    "cto-model",
		SupportedModels: "cto-model",
	}
	require.NoError(t, database.Create(&ctoProvider).Error)
	var ctoAgent db.Agent
	require.NoError(t, database.First(&ctoAgent, "company_id = ? AND role_key = ?", parentAgent.CompanyID, "CTO").Error)
	ctoAgent.SystemPrompt = "You are the configured CTO."
	ctoAgent.ProviderID = &ctoProvider.ID
	ctoAgent.Model = "cto-model"
	ctoAgent.Permissions = `{"read":"deny","grep":"deny"}`
	require.NoError(t, database.Save(&ctoAgent).Error)

	eng := engine.NewNativeEngine(database, eventhub.NewHub())
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	q := db.New(database)
	parentRunID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	assert.Equal(t, "completed", waitForRunDone(t, q, parentRunID, 30*time.Second).Status)
	subtask := waitForSubtask(t, database, task.ID, 5*time.Second)
	require.NotNil(t, subtask.AgentID)
	assert.Equal(t, ctoAgent.ID, *subtask.AgentID, "delegation must bind the child to the requested Agent row")

	requestsMu.Lock()
	gotRequests := append([]capturedRequest(nil), requests...)
	requestsMu.Unlock()
	require.GreaterOrEqual(t, len(gotRequests), 2)
	assert.Equal(t, "cto-model", gotRequests[1].Model, "the child must use the CTO Agent's model")
	var childSystemPrompt string
	for _, message := range gotRequests[1].Messages {
		if message.Role == "system" {
			childSystemPrompt = message.Content
			break
		}
	}
	assert.Contains(t, childSystemPrompt, "You are the configured CTO.", "the child prompt must come from the database Agent")

	childTools := make(map[string]bool, len(gotRequests[1].Tools))
	for _, tool := range gotRequests[1].Tools {
		childTools[tool.Function.Name] = true
	}
	assert.True(t, childTools["ls"], "the child should retain explicitly allowed tools")
	assert.False(t, childTools["read"], "the child must not receive a denied tool")
	assert.False(t, childTools["grep"], "the child must not receive a denied tool")
}

// TestNativeEngineDelegationDepthLimit verifies the two-level depth cap:
// the root (CEO) delegates to the CTO, the CTO delegates to a Coder, but the
// Coder session has no create_subtask tool and cannot nest any deeper.
func TestNativeEngineDelegationDepthLimit(t *testing.T) {
	var count atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch count.Add(1) {
		case 1:
			// Root (CEO) session delegates to the CTO.
			json.NewEncoder(w).Encode(toolCallJSON("nd-001", "create_subtask",
				`{"title":"sub1","description":"d1","agent_name":"CTO"}`))
		case 2:
			// CTO session (depth 1) delegates to a Coder.
			json.NewEncoder(w).Encode(toolCallJSON("nd-002", "create_subtask",
				`{"title":"sub2","description":"d2","agent_name":"Coder"}`))
		case 3:
			// Coder session (depth 2) tries to delegate — the tool is not
			// registered at this depth, so the call fails and the loop continues.
			json.NewEncoder(w).Encode(toolCallJSON("nd-003", "create_subtask",
				`{"title":"great-grandchild","description":"nope","agent_name":"QA"}`))
		case 4:
			// Coder finishes.
			json.NewEncoder(w).Encode(toolCallJSON("nd-004", "finish_task",
				`{"task_status":"done","finish_status":"done without delegating"}`))
		case 5:
			// CTO finishes.
			json.NewEncoder(w).Encode(toolCallJSON("nd-005", "finish_task",
				`{"task_status":"done","finish_status":"cto done"}`))
		default:
			// Root finishes.
			json.NewEncoder(w).Encode(toolCallJSON("nd-006", "finish_task",
				`{"task_status":"in-review","finish_status":"root done"}`))
		}
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

	// Depth 1: the CTO subtask exists. Depth 2: the Coder subtask exists.
	ctoTask := waitForSubtask(t, database, task.ID, 5*time.Second)
	require.NotNil(t, ctoTask.AgentID)
	var ctoAgent db.Agent
	require.NoError(t, database.First(&ctoAgent, "id = ?", *ctoTask.AgentID).Error)
	assert.Equal(t, "CTO", ctoAgent.RoleKey)
	coderTask := waitForSubtask(t, database, ctoTask.ID, 5*time.Second)
	require.NotNil(t, coderTask.AgentID)
	var coderAgent db.Agent
	require.NoError(t, database.First(&coderAgent, "id = ?", *coderTask.AgentID).Error)
	assert.Equal(t, "Coder", coderAgent.RoleKey)

	// Depth 3 must not exist: the Coder's delegation attempt was rejected.
	greatGrandchildren, err := q.ListSubtasksByParent(context.Background(), coderTask.ID)
	require.NoError(t, err)
	assert.Empty(t, greatGrandchildren, "depth-2 sessions must not be able to create subtasks")
}

// TestNativeEngineSubagentRestriction verifies that create_subtask only
// accepts agents from the delegating config's Subagents list: the CEO cannot
// delegate straight to a Coder.
func TestNativeEngineSubagentRestriction(t *testing.T) {
	var sawRestrictionError atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "not one of your sub-agents") {
			sawRestrictionError.Store(true)
			json.NewEncoder(w).Encode(toolCallJSON("sr-002", "finish_task",
				`{"task_status":"blocked","finish_status":"cannot delegate to Coder"}`))
			return
		}
		json.NewEncoder(w).Encode(toolCallJSON("sr-001", "create_subtask",
			`{"title":"direct to coder","description":"d","agent_name":"Coder"}`))
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

	assert.True(t, sawRestrictionError.Load(), "CEO delegation to a non-subagent must be rejected")
	subtasks, err := q.ListSubtasksByParent(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Empty(t, subtasks, "no subtask should be created for a rejected agent")
}

// TestNativeEngineEmptySubagentsDisablesDelegation verifies that an empty
// database subagents list is an explicit permission boundary, not a request
// to infer every other company agent.
func TestNativeEngineEmptySubagentsDisablesDelegation(t *testing.T) {
	var sawDelegationTool atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "create_subtask") {
			sawDelegationTool.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toolCallJSON("no-delegate-001", "finish_task",
			`{"task_status":"done","finish_status":"delegation is disabled"}`))
	})

	mockSrv := startTestServer(t, handler)
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)
	require.NoError(t, database.Model(&db.Agent{}).
		Where("company_id = ? AND role_key = ?", task.CompanyID, "CEO").
		Update("subagents", "").Error)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	q := db.New(database)
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	waitForRunDone(t, q, runID, 30*time.Second)
	assert.False(t, sawDelegationTool.Load(), "empty subagents must not register create_subtask")
}

// TestNativeEngineAskTaskOwner covers the question/answer loop between a
// delegated sub-agent and its task owner: the sub-agent pauses on
// ask_task_owner, the owner receives the question as the create_subtask
// result, answers via answer_subtask_question, and the sub-agent resumes with
// the answer and finishes.
func TestNativeEngineAskTaskOwner(t *testing.T) {
	var count atomic.Int32
	var questionToolResult atomic.Value // what the owner saw from create_subtask
	var answerToolResult atomic.Value   // what the sub-agent saw from ask_task_owner
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		body := string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		switch count.Add(1) {
		case 1:
			// Owner (CEO) delegates to the CTO.
			json.NewEncoder(w).Encode(toolCallJSON("ato-001", "create_subtask",
				`{"title":"pick storage","description":"decide and document the storage layer","agent_name":"CTO"}`))
		case 2:
			// CTO session asks its owner a question and pauses.
			json.NewEncoder(w).Encode(toolCallJSON("ato-002", "ask_task_owner",
				`{"question":"Which storage should I use?"}`))
		case 3:
			// Owner turn: the create_subtask result carries the question.
			questionToolResult.Store(lastToolResult(body))
			json.NewEncoder(w).Encode(toolCallJSON("ato-003", "answer_subtask_question",
				`{"answer":"Use SQLite."}`))
		case 4:
			// CTO resumed: the ask_task_owner result carries the answer.
			answerToolResult.Store(lastToolResult(body))
			json.NewEncoder(w).Encode(toolCallJSON("ato-004", "finish_task",
				`{"task_status":"done","finish_status":"Storage decided: SQLite.","result_details":"Chose SQLite per owner's answer."}`))
		default:
			// Owner wraps up after the answer_subtask_question result.
			json.NewEncoder(w).Encode(toolCallJSON("ato-005", "finish_task",
				`{"task_status":"in-review","finish_status":"Subtask answered and completed."}`))
		}
	})

	mockSrv := startTestServer(t, handler)
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	q := db.New(database)
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	run := waitForRunDone(t, q, runID, 30*time.Second)
	assert.Equal(t, "completed", run.Status)

	// The owner saw the sub-agent's question as the create_subtask result.
	qres, _ := questionToolResult.Load().(string)
	assert.Contains(t, qres, "Which storage should I use?")
	assert.Contains(t, qres, "answer_subtask_question")

	// The sub-agent saw the owner's answer as the ask_task_owner result.
	ares, _ := answerToolResult.Load().(string)
	assert.Contains(t, ares, "Use SQLite.")

	// The subtask completed, and the final answer_subtask_question result
	// carried the sub-agent's result back to the owner.
	subtask := waitForSubtask(t, database, task.ID, 5*time.Second)
	assert.Equal(t, "done", subtask.Status)

	// The Q&A exchange is recorded as comments on the subtask.
	comments, err := q.ListCommentsByTask(context.Background(), subtask.ID)
	require.NoError(t, err)
	var sawQuestion, sawAnswer bool
	for _, c := range comments {
		if c.CommentType == "ask_owner" && strings.Contains(c.Content, "Which storage") {
			sawQuestion = true
		}
		if c.CommentType == "owner_answer" && strings.Contains(c.Content, "Use SQLite.") {
			sawAnswer = true
		}
	}
	assert.True(t, sawQuestion, "ask_owner comment should be recorded on the subtask")
	assert.True(t, sawAnswer, "owner_answer comment should be recorded on the subtask")
}

// TestNativeEngineAskArtifact verifies the artifact Q&A flow: the agent asks
// a question about an artifact via ask_artifact, the engine answers it with a
// separate one-shot LLM call on the built-in Utility model group (the
// artifact content goes into the reader call, never into the asking
// session), and the short answer comes back as the tool result.
func TestNativeEngineAskArtifact(t *testing.T) {
	var count atomic.Int32
	var readerRequest atomic.Value // body of the one-shot reader call
	var answerToolResult atomic.Value
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		body := string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		switch count.Add(1) {
		case 1:
			// Root agent asks about the seeded artifact.
			json.NewEncoder(w).Encode(toolCallJSON("aa-001", "ask_artifact",
				`{"filename":"plan.md","question":"Does the document contain a Roadmap section?"}`))
		case 2:
			// The reader call: plain completion carrying the artifact + question.
			readerRequest.Store(body)
			json.NewEncoder(w).Encode(textJSON("aa-002", "Yes — the document has a \"## Roadmap\" section listing three milestones."))
		default:
			answerToolResult.Store(lastToolResult(body))
			json.NewEncoder(w).Encode(toolCallJSON("aa-003", "finish_task",
				`{"task_status":"in-review","finish_status":"Verified the plan has a roadmap."}`))
		}
	})

	mockSrv := startTestServer(t, handler)
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)
	q := db.New(database)

	// Isolated settings/data home for this test's log-file assertions below.
	tmpHome := t.TempDir()
	t.Setenv("E2E_HEADCOUNT1_HOME", tmpHome)
	headcount1Dir := filepath.Join(tmpHome, ".headcount1")
	require.NoError(t, os.MkdirAll(headcount1Dir, 0755))

	// Configure the "ask_artifact" Default Model to point at a model group
	// (any provider/model; here the same provider, cheaper model) — this is
	// what resolveDefaultModel resolves.
	var provider db.LLMProvider
	require.NoError(t, database.First(&provider, "name = ?", "mock-provider").Error)
	utilityGroup, err := q.CreateModelGroup(context.Background(), db.ModelGroup{
		Name: "Utility", Slug: "utility",
	})
	require.NoError(t, err)
	require.NoError(t, q.ReplaceModelGroupMembers(context.Background(), utilityGroup.ID, []db.ModelGroupMember{
		{ProviderID: provider.ID, Model: "cheap-model"},
	}))
	// Default Models are per-user: give the task's company an owner and
	// register the setting under that owner.
	owner, err := q.CreateUser(context.Background(), "owner@test.local")
	require.NoError(t, err)
	var comp db.Company
	require.NoError(t, database.First(&comp, task.CompanyID).Error)
	require.NoError(t, database.Model(&comp).Update("user_id", owner.ID).Error)
	require.NoError(t, database.Create(&db.DefaultModelSetting{
		Purpose: db.PurposeAskArtifact, ModelGroupID: &utilityGroup.ID, UserID: &owner.ID,
	}).Error)

	seedRun, err := q.CreateRun(context.Background(), db.Run{TaskID: task.ID, AgentID: *task.AgentID, Status: "completed"})
	require.NoError(t, err)
	_, err = q.CreateArtifact(context.Background(), db.Artifact{
		TaskID: task.ID, RunID: seedRun.ID, Filename: "plan.md", FilePath: "/x/plan.md",
		Content: "# Plan\n\n## Roadmap\n- m1\n- m2\n- m3\n",
	})
	require.NoError(t, err)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	// The seeded run is older; wait for the engine's run specifically.
	require.Eventually(t, func() bool {
		var id sql.NullInt64
		if err := database.Raw("SELECT id FROM runs WHERE task_id = ? AND id > ? ORDER BY id DESC LIMIT 1", task.ID, seedRun.ID).Scan(&id).Error; err == nil && id.Valid {
			runID = int32(id.Int64)
			return true
		}
		return false
	}, 10*time.Second, 50*time.Millisecond)
	run := waitForRunDone(t, q, runID, 30*time.Second)
	assert.Equal(t, "completed", run.Status)

	// The reader call used the utility model and carried the artifact content
	// plus the question — but no tool definitions (it's a one-shot call).
	reader, _ := readerRequest.Load().(string)
	assert.Contains(t, reader, `"model":"cheap-model"`)
	assert.Contains(t, reader, "## Roadmap")
	assert.Contains(t, reader, "Does the document contain a Roadmap section?")

	// The asking session received only the short answer.
	answer, _ := answerToolResult.Load().(string)
	assert.Contains(t, answer, "three milestones")
	assert.NotContains(t, answer, "- m1", "raw artifact content must not leak into the asking session")

	// The reader exchange was persisted to the normal JSONL run log.
	var jsonlLogs []string
	require.NoError(t, filepath.WalkDir(headcount1Dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			jsonlLogs = append(jsonlLogs, path)
		}
		return nil
	}))
	require.NotEmpty(t, jsonlLogs)
	var logContent string
	for _, path := range jsonlLogs {
		contents, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		logContent += string(contents)
	}
	assert.Contains(t, logContent, "cheap-model")
	assert.Contains(t, logContent, "Does the document contain a Roadmap section?")
	assert.Contains(t, logContent, "## Roadmap")
	assert.Contains(t, logContent, "three milestones")
}

// TestNativeEngineCreateTaskOnBoard verifies create_task: a new TOP-LEVEL
// task appears on the board with its own ref key and the requested params,
// and nothing is executed for it while it sits in the backlog.
func TestNativeEngineCreateTaskOnBoard(t *testing.T) {
	var count atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch count.Add(1) {
		case 1:
			json.NewEncoder(w).Encode(toolCallJSON("ct-001", "create_task",
				`{"title":"Plan marketing launch","description":"Prepare the launch plan for Q4.","priority":"High","due_date":"2026-09-01T00:00:00Z"}`))
		default:
			json.NewEncoder(w).Encode(toolCallJSON("ct-002", "finish_task",
				`{"task_status":"done","finish_status":"Follow-up task planned."}`))
		}
	})

	mockSrv := startTestServer(t, handler)
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)
	q := db.New(database)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	waitForRunDone(t, q, runID, 30*time.Second)

	// The new task is top-level: own ref key, no parent, requested params.
	var created db.Task
	require.NoError(t, database.First(&created, "title = ?", "Plan marketing launch").Error)
	assert.Nil(t, created.ParentID, "create_task must create a TOP-LEVEL task")
	assert.Equal(t, "backlog", created.Status)
	assert.Equal(t, "High", created.Priority)
	assert.Equal(t, "Prepare the launch plan for Q4.", created.Description)
	assert.Regexp(t, `^[A-Z]+-\d+$`, created.RefKey, "top-level tasks get a company-level ref key")
	require.NotNil(t, created.DueDate)
	assert.Equal(t, 2026, created.DueDate.Year())

	// Backlog tasks are not executed.
	var runCount int64
	require.NoError(t, database.Model(&db.Run{}).Where("task_id = ?", created.ID).Count(&runCount).Error)
	assert.Zero(t, runCount, "a backlog task must not start running")
}

// TestNativeEngineCreateTaskToDoStartsRun verifies that a task created via
// create_task with status "to-do" starts executing as an independent root
// run, decoupled from the creating session.
func TestNativeEngineCreateTaskToDoStartsRun(t *testing.T) {
	var creatorCalls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		body := string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		// The spawned task's session carries its own task header in the first
		// user message; the creator session never has that exact string.
		if strings.Contains(body, "Task: Spawned work") {
			json.NewEncoder(w).Encode(toolCallJSON("sp-001", "finish_task",
				`{"task_status":"done","finish_status":"Spawned work finished."}`))
			return
		}
		switch creatorCalls.Add(1) {
		case 1:
			json.NewEncoder(w).Encode(toolCallJSON("ct-101", "create_task",
				`{"title":"Spawned work","description":"Do it now.","status":"to-do"}`))
		default:
			json.NewEncoder(w).Encode(toolCallJSON("ct-102", "finish_task",
				`{"task_status":"done","finish_status":"Spawned and moving on."}`))
		}
	})

	mockSrv := startTestServer(t, handler)
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)
	q := db.New(database)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	waitForRunDone(t, q, runID, 30*time.Second)

	var created db.Task
	require.NoError(t, database.First(&created, "title = ?", "Spawned work").Error)
	assert.Nil(t, created.ParentID)

	// The spawned task runs to completion on its own root run.
	spawnedRunID := waitForRunCreated(t, database, created.ID, 10*time.Second)
	spawnedRun := waitForRunDone(t, q, spawnedRunID, 30*time.Second)
	assert.Equal(t, "completed", spawnedRun.Status)
	assert.Nil(t, spawnedRun.ParentRunID, "created tasks run as independent root runs")
	require.Eventually(t, func() bool {
		updated, err := q.GetTask(context.Background(), created.ID)
		return err == nil && updated.Status == "done"
	}, 10*time.Second, 100*time.Millisecond, "spawned task should finish as done")
}

// lastToolResult extracts the content of the last role:"tool" message in an
// OpenAI-style chat completions request body.
func lastToolResult(body string) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(body), &req) != nil {
		return ""
	}
	last := ""
	for _, m := range req.Messages {
		if m.Role == "tool" {
			last = m.Content
		}
	}
	return last
}

// TestNativeEngineSequentialDelegations verifies that two create_subtask calls
// in one assistant turn run one after another (delegation blocks), each
// producing its own completed subtask and linked session run.
func TestNativeEngineSequentialDelegations(t *testing.T) {
	var callCount atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch callCount.Add(1) {
		case 1:
			// Parent turn: two delegations in a single assistant message.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "chatcmpl-seq-001", "model": "test-model",
				"choices": []map[string]interface{}{{
					"message": map[string]interface{}{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]interface{}{
							{
								"id": "call_d1", "type": "function",
								"function": map[string]interface{}{
									"name":      "create_subtask",
									"arguments": `{"title":"sub1","description":"d1","agent_name":"CTO"}`,
								},
							},
							{
								"id": "call_d2", "type": "function",
								"function": map[string]interface{}{
									"name":      "create_subtask",
									"arguments": `{"title":"sub2","description":"d2","agent_name":"Designer"}`,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
			})
		case 2, 4:
			// Child sessions: finish their subtask.
			json.NewEncoder(w).Encode(toolCallJSON("seq-fin", "finish_task",
				`{"task_status":"done","finish_status":"done"}`))
		case 3, 5:
			json.NewEncoder(w).Encode(textJSON("seq-txt", "child done"))
		default:
			json.NewEncoder(w).Encode(textJSON("seq-end", "all delegations done"))
		}
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
	waitForRunDone(t, q, runID, 30*time.Second)

	// Both delegations should have produced completed subtasks.
	subtasks, err := q.ListSubtasksByParent(context.Background(), task.ID)
	require.NoError(t, err)
	require.Len(t, subtasks, 2, "both delegations should have created subtasks")
	for _, st := range subtasks {
		assert.Equal(t, "done", st.Status)
	}

	// Both child runs must be linked to the parent run.
	childRuns, err := q.ListChildRuns(context.Background(), runID)
	require.NoError(t, err)
	require.Len(t, childRuns, 2)
	for _, cr := range childRuns {
		assert.Equal(t, "completed", cr.Status)
		require.NotNil(t, cr.RootRunID)
		assert.Equal(t, runID, *cr.RootRunID)
	}
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

// TestNativeEngineAskHuman verifies that ask_human posts an ask_user comment,
// waits for a human reply, and feeds the reply back to the LLM as tool output.
func TestNativeEngineAskHuman(t *testing.T) {
	var count atomic.Int32
	var capturedToolResult atomic.Value
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := count.Add(1)
		if n == 1 {
			json.NewEncoder(w).Encode(toolCallJSON("ask-001", "ask_human",
				`{"question":"Which option should I pick?"}`))
			return
		}
		// Capture the tool result carrying the human reply.
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
		json.NewEncoder(w).Encode(toolCallJSON("finish-002", "finish_task",
			`{"task_status":"done","finish_status":"Understood, going with Option B."}`))
	})

	mockSrv := startTestServer(t, handler)
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	q := db.New(database)

	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	// Wait for the agent's question to appear as an ask_user comment.
	require.Eventually(t, func() bool {
		comments, err := q.ListCommentsByTask(context.Background(), task.ID)
		if err != nil {
			return false
		}
		for _, c := range comments {
			if c.CommentType == "ask_user" && c.Content == "Which option should I pick?" {
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "ask_user comment should have been posted")

	// Reply as the human.
	_, err := q.CreateComment(context.Background(), db.Comment{
		TaskID:     task.ID,
		AuthorType: "human",
		Content:    "Option B please",
	})
	require.NoError(t, err)

	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	run := waitForRunDone(t, q, runID, 30*time.Second)
	assert.Equal(t, "completed", run.Status)

	toolResult, _ := capturedToolResult.Load().(string)
	assert.Contains(t, toolResult, "Option B please", "human reply should be returned as the tool result")
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

// TestNativeEngineProcessTaskIgnoresTerminalStatuses verifies that a plain
// status change (e.g. dragging a card to done/blocked/in-review)
// does NOT restart the agent — only an explicit re-run may do that.
func TestNativeEngineProcessTaskIgnoresTerminalStatuses(t *testing.T) {
	mockSrv := startTestServer(t, toolCallThenTextHandler(t))
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	q := db.New(database)

	for _, status := range []string{db.TaskStatusInReview, db.TaskStatusBlocked, db.TaskStatusDone} {
		task.Status = status
		_, err := q.UpdateTask(context.Background(), task)
		require.NoError(t, err)

		require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

		// Give a would-be run goroutine time to appear, then assert nothing happened.
		time.Sleep(300 * time.Millisecond)

		var runCount int64
		require.NoError(t, database.Model(&db.Run{}).Where("task_id = ?", task.ID).Count(&runCount).Error)
		assert.Zero(t, runCount, "status %q: ProcessTask must not start a run", status)

		updated, err := q.GetTask(context.Background(), task.ID)
		require.NoError(t, err)
		assert.Equal(t, status, updated.Status, "status %q: ProcessTask must not change the status", status)
	}
}

// TestNativeEngineRerunTaskFromTerminalStatus verifies that an explicit re-run
// (Re-run button / Run Agent comment) pulls a task out of a terminal status,
// moves it to in-progress, and starts a new run.
func TestNativeEngineRerunTaskFromTerminalStatus(t *testing.T) {
	mockSrv := startTestServer(t, toolCallThenTextHandler(t))
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	q := db.New(database)

	task.Status = "done"
	_, err := q.UpdateTask(context.Background(), task)
	require.NoError(t, err)

	require.NoError(t, eng.RerunTask(context.Background(), task.ID))

	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	run := waitForRunDone(t, q, runID, 30*time.Second)
	assert.Equal(t, "completed", run.Status)

	// The mock agent finishes with finish_task(in-review), proving the run
	// actually executed after being forced out of "done".
	updated, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, "in-review", updated.Status)
}

func TestNativeEngineCheckStaleRunsRetiresOnlyRunningSessions(t *testing.T) {
	database := setupTestDB(t)
	company, err := db.New(database).CreateCompany(context.Background(), "Liveness Co")
	require.NoError(t, err)
	agent := db.Agent{CompanyID: company.ID, Name: "Runner", RoleKey: "CEO", ShortName: "CEO", SystemPrompt: "work"}
	require.NoError(t, database.Create(&agent).Error)
	task := db.Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "stalled", Status: "in-progress"}
	require.NoError(t, database.Create(&task).Error)
	old := time.Now().Add(-10 * time.Minute)
	running := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running", StartedAt: old, LastMessageTime: &old}
	completed := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "completed", StartedAt: old, LastMessageTime: &old}
	require.NoError(t, database.Create(&running).Error)
	require.NoError(t, database.Create(&completed).Error)
	eng := engine.NewNativeEngine(database, eventhub.NewHub())
	stale, err := eng.CheckStaleRuns(context.Background(), time.Minute)
	require.NoError(t, err)
	assert.Equal(t, []int32{running.ID}, stale)
	got, err := db.New(database).GetRun(context.Background(), running.ID)
	require.NoError(t, err)
	assert.Equal(t, db.RunStatusStale, got.Status)
	stale, err = eng.CheckStaleRuns(context.Background(), time.Minute)
	require.NoError(t, err)
	assert.Empty(t, stale, "monitor ticks must be idempotent")
	got, err = db.New(database).GetRun(context.Background(), completed.ID)
	require.NoError(t, err)
	assert.Equal(t, "completed", got.Status)
}

func TestNativeEngineLivenessMonitorRunsOnCadence(t *testing.T) {
	database := setupTestDB(t)
	company, err := db.New(database).CreateCompany(context.Background(), "Monitor Co")
	require.NoError(t, err)
	agent := db.Agent{CompanyID: company.ID, Name: "Runner", RoleKey: "CEO", ShortName: "CEO", SystemPrompt: "work"}
	require.NoError(t, database.Create(&agent).Error)
	task := db.Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "monitor me", Status: "in-progress"}
	require.NoError(t, database.Create(&task).Error)
	old := time.Now().Add(-time.Hour)
	run := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running", StartedAt: old, LastMessageTime: &old}
	require.NoError(t, database.Create(&run).Error)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.NewNativeEngine(database, eventhub.NewHub()).StartLivenessMonitor(ctx, 10*time.Millisecond, time.Minute)
	require.Eventually(t, func() bool {
		got, getErr := db.New(database).GetRun(context.Background(), run.ID)
		return getErr == nil && got.Status == db.RunStatusStale
	}, time.Second, 10*time.Millisecond)
}

func TestNativeEngineResumesFailedRunWithoutPreparedCheckpoint(t *testing.T) {
	mockSrv := startTestServer(t, toolCallThenTextHandler(t))
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)
	q := db.New(database)
	run, err := q.CreateRun(context.Background(), db.Run{TaskID: task.ID, AgentID: *task.AgentID, Status: "failed", StartedAt: time.Now().Add(-time.Minute)})
	require.NoError(t, err)
	// Simulate a crash after canonical messages were written but before any
	// planned pause checkpoint could be stored.
	logger, err := logging.NewSessionLoggerWithHub(t.TempDir(), "test-co", task.ID, run.ID, run.ID, nil, q)
	require.NoError(t, err)
	for _, message := range []aicli.Message{{Role: "system", Content: "resume system"}, {Role: "user", Content: "continue this work"}} {
		payload, marshalErr := json.Marshal(message)
		require.NoError(t, marshalErr)
		logger.LogConversationMessage(payload)
	}
	require.NoError(t, logger.Close())
	require.NoError(t, q.UpdateRunLogFilePath(context.Background(), run.ID, logger.FilePath()))

	eng := engine.NewNativeEngine(database, eventhub.NewHub())
	require.NoError(t, eng.ResumeSession(context.Background(), run.ID, engine.ResumeOptions{Cause: engine.ResumeAfterFailure, Reason: "retry after provider error"}))
	finalRun := waitForRunDone(t, q, run.ID, 20*time.Second)
	assert.Equal(t, "completed", finalRun.Status)
	assert.GreaterOrEqual(t, finalRun.Recovery.ResumeAttempts, 1)
	entries := readJSONLEntries(t, logger.FilePath())
	var messages []string
	for _, entry := range entries {
		if entry["type"] == "message" {
			messages = append(messages, entry["content"].(string))
		}
	}
	assert.Contains(t, messages, `{"role":"system","content":"resume system"}`)
	assert.Contains(t, messages, `{"role":"user","content":"continue this work"}`)
}
