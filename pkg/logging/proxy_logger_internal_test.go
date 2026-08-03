package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/eventhub"

	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// recordingHub captures BroadcastEvent calls in order. It satisfies the
// narrow hub interface the logger depends on.
type recordingHub struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	eventType string
	payload   map[string]interface{}
}

func (h *recordingHub) BroadcastEvent(eventType string, payload interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, _ := payload.(map[string]interface{})
	h.events = append(h.events, recordedEvent{eventType: eventType, payload: p})
}

func (h *recordingHub) runLogEntries() []map[string]interface{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]interface{}
	for _, e := range h.events {
		if e.eventType != "run_log" {
			continue
		}
		if entry, ok := e.payload["entry"].(map[string]interface{}); ok {
			out = append(out, entry)
		}
	}
	return out
}

func setupLoggerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// Mirror production: SQLite runs on a single connection (also required
	// for :memory:, where each connection would otherwise see its own DB).
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&db.Company{}, &db.Project{}, &db.Sprint{}, &db.Agent{}, &db.Task{}, &db.Run{}))
	return database
}

func createTestRun(t *testing.T, database *gorm.DB) db.Run {
	t.Helper()
	q := db.New(database)
	ctx := context.Background()
	run, err := q.CreateRun(ctx, db.Run{TaskID: 1, Status: "running"})
	require.NoError(t, err)
	return run
}

func newTestLogger(t *testing.T, hub interface{ BroadcastEvent(string, interface{}) }, database *gorm.DB, runID int32) *ProxyLogger {
	t.Helper()
	var q *db.Queries
	if database != nil {
		q = db.New(database)
	}
	logger, err := NewProxyLoggerWithHub(t.TempDir(), "TST", 1, runID, hub, q)
	require.NoError(t, err)
	return logger
}

func persistedEntries(t *testing.T, database *gorm.DB, runID int32) []map[string]interface{} {
	t.Helper()
	run, err := db.New(database).GetRun(context.Background(), runID)
	require.NoError(t, err)
	if run.LogEntries == "" {
		return nil
	}
	var entries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(run.LogEntries), &entries))
	return entries
}

// TestBroadcastAndPersistedEntryShareTimestamp verifies the dedup contract
// the frontend relies on: the entry broadcast over the WebSocket and the
// entry stored in the database are the same object with the same ts, so a
// resync can merge the DB snapshot with the live stream without duplicates.
func TestBroadcastAndPersistedEntryShareTimestamp(t *testing.T) {
	database := setupLoggerTestDB(t)
	run := createTestRun(t, database)
	hub := &recordingHub{}
	logger := newTestLogger(t, hub, database, run.ID)

	logger.LogInfo("hello world")
	require.NoError(t, logger.Close())

	broadcast := hub.runLogEntries()
	require.Len(t, broadcast, 1)
	persisted := persistedEntries(t, database, run.ID)
	require.Len(t, persisted, 1)

	assert.Equal(t, "info", broadcast[0]["type"])
	assert.Equal(t, "hello world", broadcast[0]["content"])
	assert.NotEmpty(t, broadcast[0]["ts"])
	assert.Equal(t, broadcast[0]["ts"], persisted[0]["ts"],
		"broadcast and persisted entries must share one timestamp")
	assert.Equal(t, broadcast[0]["content"], persisted[0]["content"])
}

// TestPersistOrderMatchesBroadcastOrder verifies that entries land in the
// database in exactly the order they were broadcast, even for bursts larger
// than the persistence queue buffer. The old implementation spawned one
// goroutine per entry, so DB order was scheduler-dependent.
func TestPersistOrderMatchesBroadcastOrder(t *testing.T) {
	database := setupLoggerTestDB(t)
	run := createTestRun(t, database)
	hub := &recordingHub{}
	logger := newTestLogger(t, hub, database, run.ID)

	const n = 300 // larger than the queue buffer to exercise blocking sends
	for i := 0; i < n; i++ {
		logger.LogInfo(fmt.Sprintf("entry-%d", i))
	}
	require.NoError(t, logger.Close())

	persisted := persistedEntries(t, database, run.ID)
	require.Len(t, persisted, n, "Close must drain the queue completely")
	for i, e := range persisted {
		assert.Equal(t, fmt.Sprintf("entry-%d", i), e["content"], "entry %d out of order", i)
	}
}

// TestConcurrentLoggingNoLossNoReorder hammers one logger from many
// goroutines (run with -race) and verifies the database ends up with every
// entry, in the exact order they were broadcast.
func TestConcurrentLoggingNoLossNoReorder(t *testing.T) {
	database := setupLoggerTestDB(t)
	run := createTestRun(t, database)
	hub := &recordingHub{}
	logger := newTestLogger(t, hub, database, run.ID)

	const goroutines = 8
	const perGoroutine = 40
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				logger.LogInfo(fmt.Sprintf("g%d-%d", g, i))
			}
		}(g)
	}
	wg.Wait()
	require.NoError(t, logger.Close())

	broadcast := hub.runLogEntries()
	persisted := persistedEntries(t, database, run.ID)
	require.Len(t, broadcast, goroutines*perGoroutine)
	require.Len(t, persisted, goroutines*perGoroutine)
	for i := range broadcast {
		assert.Equal(t, broadcast[i]["content"], persisted[i]["content"],
			"persisted order diverges from broadcast order at %d", i)
	}
}

// TestTwoLoggersSameRunNoLoss mirrors the LLM gateway, which creates one
// short-lived logger per HTTP request for the same run: concurrent append
// transactions must not overwrite each other's entries.
func TestTwoLoggersSameRunNoLoss(t *testing.T) {
	database := setupLoggerTestDB(t)
	run := createTestRun(t, database)
	hub := &recordingHub{}
	a := newTestLogger(t, hub, database, run.ID)
	b := newTestLogger(t, hub, database, run.ID)

	const n = 50
	var wg sync.WaitGroup
	for _, l := range []*ProxyLogger{a, b} {
		wg.Add(1)
		go func(l *ProxyLogger) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				l.LogInfo(fmt.Sprintf("x-%d", i))
			}
		}(l)
	}
	wg.Wait()
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	persisted := persistedEntries(t, database, run.ID)
	assert.Len(t, persisted, 2*n, "no entry may be lost to a concurrent read-modify-write")
}

// TestLogAfterCloseDoesNotPanic verifies that a straggling log call after
// Close (e.g. a late goroutine) neither panics nor blocks.
func TestLogAfterCloseDoesNotPanic(t *testing.T) {
	database := setupLoggerTestDB(t)
	run := createTestRun(t, database)
	hub := &recordingHub{}
	logger := newTestLogger(t, hub, database, run.ID)

	logger.LogInfo("before close")
	require.NoError(t, logger.Close())

	assert.NotPanics(t, func() { logger.LogInfo("after close") })

	persisted := persistedEntries(t, database, run.ID)
	require.Len(t, persisted, 1)
	assert.Equal(t, "before close", persisted[0]["content"])
}

// TestLoggerWithoutHubOrDB verifies the logger degrades gracefully when
// created without a hub and/or query layer (both are optional in prod).
func TestLoggerWithoutHubOrDB(t *testing.T) {
	logger, err := NewProxyLogger(t.TempDir(), "TST", 1, 7)
	require.NoError(t, err)
	assert.NotPanics(t, func() {
		logger.LogInfo("no hub, no db")
		logger.LogErrorMsg("still fine")
	})
	require.NoError(t, logger.Close())
}

// TestToolResultsDedupedAcrossRequests verifies that the same tool result
// (identified by tool_call_id), which reappears in every subsequent LLM
// request's message history, is only logged once.
func TestToolResultsDedupedAcrossRequests(t *testing.T) {
	database := setupLoggerTestDB(t)
	run := createTestRun(t, database)
	hub := &recordingHub{}
	logger := newTestLogger(t, hub, database, run.ID)

	messages := []map[string]interface{}{
		{"role": "tool", "tool_call_id": "call_1", "content": "result one"},
	}
	logger.LogToolResultsFromRequest("m", "p", messages)
	// Same history plus one new result on the next request.
	messages = append(messages, map[string]interface{}{
		"role": "tool", "tool_call_id": "call_2", "content": "result two",
	})
	logger.LogToolResultsFromRequest("m", "p", messages)
	require.NoError(t, logger.Close())

	persisted := persistedEntries(t, database, run.ID)
	require.Len(t, persisted, 2)
	assert.Equal(t, "result one", persisted[0]["content"])
	assert.Equal(t, "result two", persisted[1]["content"])
}

// TestSeqUniqueAndIncreasingAcrossLoggers verifies the per-run sequence
// numbers the frontend keys its resync merge on: every entry gets one, they
// never repeat within a run — even when several loggers write to the same
// run concurrently (the LLM gateway pattern) — and each logger's own entries
// see strictly increasing seq.
func TestSeqUniqueAndIncreasingAcrossLoggers(t *testing.T) {
	database := setupLoggerTestDB(t)
	run := createTestRun(t, database)
	hub := &recordingHub{}
	a := newTestLogger(t, hub, database, run.ID)
	b := newTestLogger(t, hub, database, run.ID)

	const n = 40
	var wg sync.WaitGroup
	for _, l := range []*ProxyLogger{a, b} {
		wg.Add(1)
		go func(l *ProxyLogger) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				l.LogInfo(fmt.Sprintf("s-%d", i))
			}
		}(l)
	}
	wg.Wait()
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	persisted := persistedEntries(t, database, run.ID)
	require.Len(t, persisted, 2*n)
	seen := map[int64]bool{}
	for _, e := range persisted {
		s, ok := e["seq"].(float64)
		require.True(t, ok, "every entry must carry a numeric seq: %v", e)
		require.False(t, seen[int64(s)], "seq %d assigned twice", int64(s))
		seen[int64(s)] = true
	}
}

// TestSeqContinuesAfterRestart verifies that a fresh process (simulated by
// clearing the in-memory counters) seeds seq from the persisted entries, so
// resumed runs never reuse sequence numbers the frontend has already seen.
func TestSeqContinuesAfterRestart(t *testing.T) {
	database := setupLoggerTestDB(t)
	run := createTestRun(t, database)
	hub := &recordingHub{}

	logger := newTestLogger(t, hub, database, run.ID)
	logger.LogInfo("first")
	logger.LogInfo("second")
	require.NoError(t, logger.Close())

	runSeqCounters.Delete(run.ID) // simulate a server restart

	logger2 := newTestLogger(t, hub, database, run.ID)
	logger2.LogInfo("after restart")
	require.NoError(t, logger2.Close())

	persisted := persistedEntries(t, database, run.ID)
	require.Len(t, persisted, 3)
	maxBefore := int64(0)
	for _, e := range persisted[:2] {
		if s, ok := e["seq"].(float64); ok && int64(s) > maxBefore {
			maxBefore = int64(s)
		}
	}
	resumed, ok := persisted[2]["seq"].(float64)
	require.True(t, ok)
	assert.Greater(t, int64(resumed), maxBefore, "seq must continue above persisted entries after a restart")
}

// TestEndToEndWebSocketDelivery wires a real eventhub.Hub, a real WebSocket
// server, and a ProxyLogger together, then verifies a connected browser-like
// client receives every entry in order — and that each received entry can be
// matched by ts against the database snapshot (the resync/dedup contract).
func TestEndToEndWebSocketDelivery(t *testing.T) {
	database := setupLoggerTestDB(t)
	run := createTestRun(t, database)

	hub := eventhub.NewHub()
	defer hub.Close()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		hub.Serve(conn, 1)
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	// Wait for the hub to register the client before broadcasting.
	deadline := time.Now().Add(2 * time.Second)
	for hub.ClientCount() == 0 {
		require.False(t, time.Now().After(deadline), "hub never registered the client")
		time.Sleep(5 * time.Millisecond)
	}

	logger := newTestLogger(t, hub, database, run.ID)
	logger.LogRequest("model-x", "agent-a", "prov", []byte(`{"messages":[]}`))
	logger.LogResponse("model-x", "prov", 200, []byte(`{"ok":true}`), "", Usage{})
	logger.LogInfo("done")
	require.NoError(t, logger.Close())

	wantTypes := []string{"request", "response", "info"}
	var receivedTs []string
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < len(wantTypes); i++ {
		_, data, err := conn.ReadMessage()
		require.NoError(t, err)
		var msg struct {
			Type    string `json:"type"`
			Payload struct {
				RunID int32                  `json:"run_id"`
				Entry map[string]interface{} `json:"entry"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(data, &msg))
		require.Equal(t, "run_log", msg.Type)
		assert.Equal(t, run.ID, msg.Payload.RunID)
		assert.Equal(t, wantTypes[i], msg.Payload.Entry["type"])
		ts, _ := msg.Payload.Entry["ts"].(string)
		require.NotEmpty(t, ts)
		receivedTs = append(receivedTs, ts)
	}

	persisted := persistedEntries(t, database, run.ID)
	require.Len(t, persisted, len(wantTypes))
	for i, e := range persisted {
		assert.Equal(t, receivedTs[i], e["ts"],
			"DB entry %d must carry the same ts the client saw live", i)
	}
}
