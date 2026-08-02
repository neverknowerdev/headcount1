package logging_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/secrets/redact"

	"github.com/stretchr/testify/require"
)

// TestProxyLoggerRedactsRegisteredSecrets drives every logger entry type with
// a registered secret embedded and asserts none of the three sinks — the
// JSONL file and the persisted runs.log_entries column (the WebSocket mirror
// carries the same entry object) — ever contains the plaintext.
func TestProxyLoggerRedactsRegisteredSecrets(t *testing.T) {
	logger, q, runID, _ := setupLoggerTest(t)

	const secret = "sk-proxytest-supersecret-value-123456"
	redact.Register("test-secret", secret)

	reqBody := `{"messages":[{"role":"system","content":"the key is ` + secret + `"}]}`
	logger.LogRequest("gpt-x", "QA", "openai", []byte(reqBody))
	logger.LogResponse("gpt-x", "openai", 200, []byte(`{"content":"echo `+secret+`"}`), "", logging.Usage{})
	logger.LogStreamResponse("gpt-x", "openai", "text "+secret, "", []map[string]interface{}{
		{"id": "call_1", "name": "bash", "arguments": map[string]interface{}{"command": "echo " + secret}},
	}, []byte("raw "+secret), logging.Usage{})
	logger.LogToolResultsFromRequest("gpt-x", "openai", []map[string]interface{}{
		{"role": "tool", "tool_call_id": "call_1", "content": "output " + secret},
	})
	logger.LogError("gpt-x", "QA", "openai", &testErr{"auth failed for " + secret})
	require.NoError(t, logger.Close())

	fileEntries := readEntries(t, logger.FilePath())
	require.NotEmpty(t, fileEntries)
	for _, e := range fileEntries {
		for k, v := range e {
			s, ok := v.(string)
			if !ok {
				continue
			}
			require.NotContains(t, s, secret, "JSONL sink leaked secret in field %q", k)
		}
	}

	run, err := q.GetRun(context.Background(), runID)
	require.NoError(t, err)
	require.NotContains(t, run.LogEntries, secret, "DB sink leaked secret")
	require.True(t, strings.Contains(run.LogEntries, "[REDACTED:") ||
		strings.Contains(string(mustFile(t, logger.FilePath())), "[REDACTED:"),
		"expected a redaction marker somewhere in the persisted output")
}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }

func mustFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}
