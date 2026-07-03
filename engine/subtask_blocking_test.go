package engine_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDelegateTaskReturnsRichResult verifies the delegation flow: the
// delegate_task tool result (visible in the parent's next LLM request)
// carries the child session's status, run key, result summary, and the
// expand_run_result pointer, and the child task/run get ref-key names.
func TestDelegateTaskReturnsRichResult(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var bodies []string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		var resp map[string]interface{}
		switch {
		case n == 1:
			// Parent turn 1: delegate to the Researcher.
			resp = toolCallResponse("delegate_task",
				`{"title":"research X","description":"Investigate X and write findings to findings.md","agent_name":"Researcher"}`)
		case n == 2:
			// Child session turn 1: finish immediately with a result.
			resp = toolCallResponse("finish_task",
				`{"task_status":"in-review","finish_status":"Research complete: X is feasible.","result_details":"Full detail about X."}`)
		default:
			// Parent turn 2 (sees the delegation result): finish the parent.
			resp = toolCallResponse("finish_task",
				`{"task_status":"in-review","finish_status":"Delegated and collected research."}`)
		}
		json.NewEncoder(w).Encode(resp)
	})

	mockSrv := startTestServer(t, handler)
	database := setupTestDB(t)
	task := seedTestData(t, database, mockSrv.URL)

	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	q := db.New(database)
	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	waitForRunDone(t, q, runID, 60*time.Second)

	// The parent's post-delegation request must contain the rich tool result.
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(bodies), 3, "expected a parent turn after the child session finished")
	last := bodies[len(bodies)-1]
	assert.Contains(t, last, "Delegated session for subtask", "delegation result missing")
	assert.Contains(t, last, "Research complete: X is feasible.", "child result summary missing")
	assert.Contains(t, last, "expand_run_result", "detailed-handoff pointer missing")

	// The child task ended in-review with the detailed handoff stored, and
	// both the task and its run carry human-readable keys.
	subtasks, err := q.ListSubtasksByParent(context.Background(), task.ID)
	require.NoError(t, err)
	require.Len(t, subtasks, 1)
	assert.Equal(t, "in-review", subtasks[0].Status)
	assert.Equal(t, task.RefKey+"-1", subtasks[0].RefKey)

	childRun, err := q.GetLatestRunByTask(context.Background(), subtasks[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "Full detail about X.", childRun.ResultExplanation)
	assert.Equal(t, subtasks[0].RefKey+"-RSRCH", childRun.Name)
}

// TestDelegateTaskRejectsAbsolutePaths: delegation descriptions pointing at
// paths the sandbox forbids fail fast instead of wasting a whole session.
func TestDelegateTaskRejectsAbsolutePaths(t *testing.T) {
	var sawError atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "cannot access") {
			sawError.Store(true)
			json.NewEncoder(w).Encode(toolCallResponse("finish_task",
				`{"task_status":"blocked","finish_status":"path rejected"}`))
			return
		}
		json.NewEncoder(w).Encode(toolCallResponse("delegate_task",
			`{"title":"explore","description":"Explore /Users/someone/project/app and report","agent_name":"Researcher"}`))
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

	assert.True(t, sawError.Load(), "agent should have seen the absolute-path rejection")
	subtasks, err := q.ListSubtasksByParent(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Empty(t, subtasks, "no subtask should be created for a rejected description")
}

// toolCallResponse builds a minimal OpenAI-style tool-call response.
func toolCallResponse(name, args string) map[string]interface{} {
	return map[string]interface{}{
		"id": "chatcmpl-" + name, "model": "test-model",
		"choices": []map[string]interface{}{{
			"message": map[string]interface{}{
				"role": "assistant", "content": "",
				"tool_calls": []map[string]interface{}{{
					"id": "call_" + name, "type": "function",
					"function": map[string]interface{}{"name": name, "arguments": args},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
}
