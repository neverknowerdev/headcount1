package logging_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/logging"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupLoggerTest(t *testing.T) (*logging.ProxyLogger, *db.Queries, int32, string) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&db.Run{}, &db.RunSnapshot{}))
	q := db.New(database)

	run, err := q.CreateRun(context.Background(), db.Run{TaskID: 1, AgentID: 1, Status: "running"})
	require.NoError(t, err)

	basePath := t.TempDir()
	logger, err := logging.NewSessionLoggerWithHub(basePath, "test-co", 1, run.ID, run.ID, nil, q)
	require.NoError(t, err)
	t.Cleanup(func() { logger.Close() })
	return logger, q, run.ID, basePath
}

func TestProxyLoggerCanonicalMessageEvent(t *testing.T) {
	logger, _, _, _ := setupLoggerTest(t)
	seq := logger.LogConversationMessage([]byte(`{"role":"assistant","content":"checkpoint"}`))
	require.Positive(t, seq)
	require.NoError(t, logger.Sync())
	entries := readEntries(t, logger.FilePath())
	require.Len(t, entries, 1)
	assert.Equal(t, "message", entries[0]["type"])
	assert.Equal(t, float64(seq), entries[0]["seq"])
	assert.Equal(t, int64(1), int64(entries[0]["message_version"].(float64)))
	assert.Equal(t, `{"role":"assistant","content":"checkpoint"}`, entries[0]["content"])
}

func readEntries(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var entries []map[string]interface{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), "every log line must be valid JSON: %q", line)
		entries = append(entries, entry)
	}
	return entries
}

// TestProxyLoggerJSONL verifies the run log file is JSONL: one structured
// entry per line with the same {type, content, ts, ...} shape the DB and
// WebSocket sinks use, full request/response bodies embedded, and tool
// results untruncated (the file doubles as a fine-tuning trajectory).
func TestProxyLoggerJSONL(t *testing.T) {
	logger, q, runID, _ := setupLoggerTest(t)

	assert.Equal(t, "main.jsonl", filepath.Base(logger.FilePath()))

	reqBody := `{"messages":[{"role":"system","content":"be helpful"},{"role":"user","content":"hi"}],"tools":[]}`
	logger.LogRequest("gpt-test", "coder", "openai", []byte(reqBody))

	respBody := `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`
	logger.LogResponse("gpt-test", "openai", 200, []byte(respBody), "", logging.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})

	// A tool result larger than the 2000-char UI preview must land in the
	// file untruncated.
	bigOutput := strings.Repeat("x", 5000)
	logger.LogToolResultsFromRequest("gpt-test", "openai", []map[string]interface{}{
		{"role": "tool", "tool_call_id": "call_1", "content": bigOutput},
	})

	logger.LogInfo("plain info line")
	logger.LogSessionStarted(2, 3, "qa", "Verify things", "session-2.jsonl")
	logger.LogSessionEnded(2, "completed", "all good")
	logger.LogModelSwitch("openai", "gpt-a", "other", "gpt-b", "rate limited")
	logger.LogOutcome("completed", "finish_task", "done", "coder", 1, "Implemented the feature.")

	entries := readEntries(t, logger.FilePath())
	require.Len(t, entries, 8)

	byType := map[string]map[string]interface{}{}
	var types []string
	for _, e := range entries {
		typ, _ := e["type"].(string)
		types = append(types, typ)
		byType[typ] = e
		assert.NotEmpty(t, e["ts"], "entry %s must carry a timestamp", typ)
	}
	assert.Equal(t, []string{"request", "response", "tool_response", "info", "session_started", "session_ended", "model_switch", "outcome"}, types)

	assert.Equal(t, reqBody, byType["request"]["content"])
	assert.Equal(t, "coder", byType["request"]["agent_name"])
	assert.Equal(t, "openai", byType["request"]["provider"])

	var respData map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(byType["response"]["content"].(string)), &respData))
	assert.Equal(t, respBody, respData["raw"])
	assert.Equal(t, float64(200), byType["response"]["status_code"])

	assert.Equal(t, bigOutput, byType["tool_response"]["content"], "file keeps the full tool output")
	assert.Equal(t, "call_1", byType["tool_response"]["tool_call_id"])

	assert.Equal(t, "session-2.jsonl", byType["session_started"]["log_file"])

	// The outcome entry — the trajectory's training label — is the last
	// line and is self-describing (run/task/agent identity embedded).
	outcome := entries[len(entries)-1]
	assert.Equal(t, "outcome", outcome["type"])
	assert.Equal(t, "completed", outcome["status"])
	assert.Equal(t, "finish_task", outcome["end_reason"])
	assert.Equal(t, "done", outcome["task_status"])
	assert.Equal(t, "Implemented the feature.", outcome["content"])
	assert.Equal(t, float64(runID), outcome["run_id"])
	assert.Equal(t, float64(1), outcome["task_id"])

	// The DB mirror gets the same entries but with the tool output capped
	// to a preview. persistEntry is async, so poll briefly.
	var dbEntries []map[string]interface{}
	require.Eventually(t, func() bool {
		run, err := q.GetRun(context.Background(), runID)
		if err != nil || run.LogEntries == "" {
			return false
		}
		dbEntries = nil
		if json.Unmarshal([]byte(run.LogEntries), &dbEntries) != nil {
			return false
		}
		return len(dbEntries) == len(entries)
	}, 5*time.Second, 50*time.Millisecond)

	for _, e := range dbEntries {
		if e["type"] == "tool_response" {
			content, _ := e["content"].(string)
			assert.True(t, strings.HasSuffix(content, "…(truncated)"), "DB mirror keeps a bounded preview")
			assert.Less(t, len(content), len(bigOutput))
		}
	}
}

// TestSessionLoggerFileNames verifies delegated child sessions log to
// session-{runID}.jsonl in the root run's folder.
func TestSessionLoggerFileNames(t *testing.T) {
	basePath := t.TempDir()
	logger, err := logging.NewSessionLoggerWithHub(basePath, "test-co", 7, 10, 11, nil, nil)
	require.NoError(t, err)
	defer logger.Close()
	assert.Equal(t, "session-11.jsonl", filepath.Base(logger.FilePath()))
	assert.Contains(t, logger.FilePath(), filepath.Join("logs", "test-co", "7", "run-10"))
}
