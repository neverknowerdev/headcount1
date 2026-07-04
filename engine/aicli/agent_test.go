package aicli_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveCredentials returns (baseURL, apiKey, model) from environment variables
// used for real-provider recording runs. Returns empty strings when not set.
func liveCredentials() (baseURL, apiKey, model string) {
	return os.Getenv("TEST_LLM_BASE_URL"),
		os.Getenv("TEST_LLM_API_KEY"),
		os.Getenv("TEST_LLM_MODEL")
}

// newTestClient creates a Client backed by a FixtureTransport for the given
// fixture file. When RECORD=true and TEST_LLM_* vars are set, it routes
// requests to the real provider and records responses.
func newTestClient(t *testing.T, fixturePath string) *aicli.Client {
	t.Helper()
	baseURL, apiKey, model := liveCredentials()

	var upstream http.RoundTripper
	if os.Getenv("RECORD") == "true" {
		if baseURL == "" {
			t.Skip("RECORD=true but TEST_LLM_BASE_URL not set")
		}
		upstream = http.DefaultTransport
	}
	if model == "" {
		model = "test-model"
	}
	if baseURL == "" {
		baseURL = "http://ignored" // overridden by fixture transport
	}

	ft := aicli.NewFixtureTransport(fixturePath, upstream)

	client := aicli.NewClient(baseURL, apiKey, model)
	// Disable built-in retry so individual test turns consume exactly one fixture entry.
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: ft}
	return client
}

// TestAgentSimpleChat verifies that the agent returns the assistant text when
// the LLM doesn't invoke any tools.
func TestAgentSimpleChat(t *testing.T) {
	fixturePath := filepath.Join("testdata", "fixtures", "simple_chat.json")
	client := newTestClient(t, fixturePath)

	reg := aicli.NewRegistry() // no tools needed for this test
	agent := aicli.New(aicli.Config{
		Client:       client,
		Registry:     reg,
		ProviderName: "test-provider",
		AgentName:    "test-agent",
	})

	result, err := agent.Run(context.Background(), "", "What is 2+2?")
	require.NoError(t, err)
	assert.Equal(t, "4", result)
}

// TestAgentToolCall verifies the two-turn tool-call loop: first response
// includes a tool call, second response is the final text answer.
func TestAgentToolCall(t *testing.T) {
	fixturePath := filepath.Join("testdata", "fixtures", "tool_call.json")
	client := newTestClient(t, fixturePath)

	// Track whether finish_task was called.
	var finishCalled atomic.Bool
	var capturedStatus string

	reg := aicli.NewRegistry()
	reg.Register(tools.NewFinishTask(false, func(ctx context.Context, status, finishStatus, resultDetails string) error {
		finishCalled.Store(true)
		capturedStatus = status
		return nil
	}))

	agent := aicli.New(aicli.Config{
		Client:       client,
		Registry:     reg,
		ProviderName: "test-provider",
		AgentName:    "test-agent",
	})

	result, err := agent.Run(context.Background(), "You are an agent.", "Complete the task and update its status.")
	require.NoError(t, err)

	assert.True(t, finishCalled.Load(), "finish_task should have been called")
	assert.Equal(t, "in-review", capturedStatus)
	assert.Contains(t, result, "in-review")
}

// TestAgentReadFileTool verifies that the agent can invoke read_file and
// incorporate the file content into its response.
func TestAgentReadFileTool(t *testing.T) {
	fixturePath := filepath.Join("testdata", "fixtures", "read_file_tool.json")
	client := newTestClient(t, fixturePath)

	// Create a temp workspace with a known file.
	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "hello.txt"), []byte("hello world"), 0644))

	reg := tools.DefaultRegistry(workDir)
	agent := aicli.New(aicli.Config{
		Client:       client,
		Registry:     reg,
		ProviderName: "test-provider",
		AgentName:    "test-agent",
	})

	result, err := agent.Run(context.Background(), "", "Read hello.txt and tell me what it says.")
	require.NoError(t, err)
	assert.Contains(t, result, "hello world")
}

// TestAgentRetryOn429 verifies that the client retries after a 429 response.
// The fixture has a 429 first, then a 200.
func TestAgentRetryOn429(t *testing.T) {
	fixturePath := filepath.Join("testdata", "fixtures", "error_retry.json")

	baseURL, apiKey, model := liveCredentials()
	var upstream http.RoundTripper
	if os.Getenv("RECORD") == "true" {
		if baseURL == "" {
			t.Skip("RECORD=true but TEST_LLM_BASE_URL not set")
		}
		upstream = http.DefaultTransport
	}
	if model == "" {
		model = "test-model"
	}
	if baseURL == "" {
		baseURL = "http://ignored"
	}

	ft := aicli.NewFixtureTransport(fixturePath, upstream)
	client := aicli.NewClient(baseURL, apiKey, model)
	// Allow one retry so the second fixture entry is used.
	client.MaxRetries = 1
	client.HTTPClient = &http.Client{Transport: ft}

	reg := aicli.NewRegistry()
	agent := aicli.New(aicli.Config{
		Client:       client,
		Registry:     reg,
		ProviderName: "test-provider",
		AgentName:    "test-agent",
	})

	result, err := agent.Run(context.Background(), "", "What is 2+2?")
	require.NoError(t, err)
	assert.Equal(t, "4", result)
}

// TestClientBodyEmbeddedError verifies that a 200 response with an error in
// the body is treated as an error (not silently ignored).
func TestClientBodyEmbeddedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"message": "model not found",
				"type":    "invalid_request_error",
				"code":    "model_not_found",
			},
		})
	}))
	defer srv.Close()

	client := aicli.NewClient(srv.URL, "", "bad-model")
	client.MaxRetries = 0

	_, _, err := client.Complete(context.Background(), aicli.ChatRequest{
		Messages: []aicli.Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model not found")
}

// TestToolReadFile verifies the read tool in isolation.
func TestToolReadFile(t *testing.T) {
	workDir := t.TempDir()
	content := "line1\nline2\nline3"
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "test.txt"), []byte(content), 0644))

	reg := tools.DefaultRegistry(workDir)

	result, err := reg.Execute(context.Background(), "read", json.RawMessage(`{"path":"test.txt"}`))
	require.NoError(t, err)
	assert.Equal(t, content, result)
}

// TestToolListDir verifies the ls tool.
func TestToolListDir(t *testing.T) {
	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("a"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "sub"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "sub", "b.txt"), []byte("b"), 0644))

	reg := tools.DefaultRegistry(workDir)

	result, err := reg.Execute(context.Background(), "ls", json.RawMessage(`{"path":".","recursive":false}`))
	require.NoError(t, err)
	assert.Contains(t, result, "a.txt")
	assert.Contains(t, result, "sub/")
	assert.NotContains(t, result, "b.txt") // non-recursive
}

// TestToolExecCommand verifies the bash tool.
func TestToolExecCommand(t *testing.T) {
	workDir := t.TempDir()
	reg := tools.DefaultRegistry(workDir)

	result, err := reg.Execute(context.Background(), "bash", json.RawMessage(`{"command":"echo hello"}`))
	require.NoError(t, err)
	assert.Equal(t, "hello\n", result)
}

// TestToolGrep verifies the grep tool.
func TestToolGrep(t *testing.T) {
	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "code.go"), []byte("package main\nfunc main() {}"), 0644))

	reg := tools.DefaultRegistry(workDir)

	result, err := reg.Execute(context.Background(), "grep", json.RawMessage(`{"pattern":"func"}`))
	require.NoError(t, err)
	assert.Contains(t, result, "code.go:2")
	assert.Contains(t, result, "func main")
}

// TestToolWorkspaceSandbox verifies that file tools reject paths that escape the
// workspace boundary — both absolute paths and ../.. traversals.
func TestToolWorkspaceSandbox(t *testing.T) {
	workDir := t.TempDir()
	reg := tools.DefaultRegistry(workDir)

	cases := []struct {
		tool string
		args string
	}{
		// read: absolute path outside workspace
		{"read", `{"path":"/etc/passwd"}`},
		// read: traversal via ../..
		{"read", `{"path":"../../etc/passwd"}`},
		// ls: parent traversal
		{"ls", `{"path":".."}`},
		// grep: absolute path outside workspace
		{"grep", `{"pattern":"root","path":"/etc"}`},
		// write: absolute path outside workspace
		{"write", `{"path":"/tmp/evil.txt","content":"hack"}`},
		// write: traversal outside workspace
		{"write", `{"path":"../evil.txt","content":"hack"}`},
		// bash: ls on absolute path outside workspace
		{"bash", `{"command":"ls /etc"}`},
		// bash: cat on absolute path outside workspace
		{"bash", `{"command":"cat /etc/passwd"}`},
		// bash: parent directory via ".."
		{"bash", `{"command":"ls .."}`},
		// bash: parent traversal in relative path
		{"bash", `{"command":"cat ../secret.txt"}`},
		// bash: embedded traversal in relative path
		{"bash", `{"command":"ls sub/../../outside"}`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.tool+"/"+tc.args, func(t *testing.T) {
			_, err := reg.Execute(context.Background(), tc.tool, json.RawMessage(tc.args))
			require.Error(t, err, "expected sandbox error for %s %s", tc.tool, tc.args)
			assert.Contains(t, err.Error(), "escapes the workspace")
		})
	}
}

// TestToolWriteFile verifies that write creates files inside the workspace
// and that read can read them back.
func TestToolWriteFile(t *testing.T) {
	workDir := t.TempDir()
	reg := tools.DefaultRegistry(workDir)

	// Write a top-level file.
	result, err := reg.Execute(context.Background(), "write",
		json.RawMessage(`{"path":"output.txt","content":"hello from agent"}`))
	require.NoError(t, err)
	assert.Contains(t, result, "output.txt")

	data, err := os.ReadFile(filepath.Join(workDir, "output.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello from agent", string(data))

	// Write in a nested subdirectory — parent dirs should be created automatically.
	_, err = reg.Execute(context.Background(), "write",
		json.RawMessage(`{"path":"sub/dir/nested.txt","content":"nested content"}`))
	require.NoError(t, err)
	data, err = os.ReadFile(filepath.Join(workDir, "sub", "dir", "nested.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nested content", string(data))

	// Round-trip: write then read back via read.
	readResult, err := reg.Execute(context.Background(), "read",
		json.RawMessage(`{"path":"output.txt"}`))
	require.NoError(t, err)
	assert.Equal(t, "hello from agent", readResult)
}

// TestAgentWriteFileTool verifies the full agent-loop path where the LLM calls
// write and the file is created in the workspace.
func TestAgentWriteFileTool(t *testing.T) {
	fixturePath := filepath.Join("testdata", "fixtures", "write_file_tool.json")
	client := newTestClient(t, fixturePath)

	workDir := t.TempDir()
	reg := tools.DefaultRegistry(workDir)
	agent := aicli.New(aicli.Config{
		Client:       client,
		Registry:     reg,
		ProviderName: "test-provider",
		AgentName:    "test-agent",
	})

	result, err := agent.Run(context.Background(), "", "Write 'task completed' to result.txt.")
	require.NoError(t, err)
	assert.Contains(t, result, "result.txt")

	data, err := os.ReadFile(filepath.Join(workDir, "result.txt"))
	require.NoError(t, err)
	assert.Equal(t, "task completed", string(data))
}

// ---- Registry.Filter tests --------------------------------------------------

// TestRegistryFilter_EmptyAllowedReturnsOriginal verifies that an empty allowed
// list returns the original registry unchanged.
func TestRegistryFilter_EmptyAllowedReturnsOriginal(t *testing.T) {
	reg := tools.DefaultRegistry(t.TempDir())
	filtered := reg.Filter(nil)
	assert.Same(t, reg, filtered)
}

// TestRegistryFilter_WildcardReturnsOriginal verifies that ["*"] returns
// the original registry.
func TestRegistryFilter_WildcardReturnsOriginal(t *testing.T) {
	reg := tools.DefaultRegistry(t.TempDir())
	filtered := reg.Filter([]string{"*"})
	assert.Same(t, reg, filtered)
}

// TestRegistryFilter_Subset verifies that a filtered registry only contains
// the listed tools.
func TestRegistryFilter_Subset(t *testing.T) {
	workDir := t.TempDir()
	reg := tools.DefaultRegistry(workDir)

	filtered := reg.Filter([]string{"read", "grep"})

	// read and grep should work.
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "f.txt"), []byte("hi"), 0644))
	_, err := filtered.Execute(context.Background(), "read", json.RawMessage(`{"path":"f.txt"}`))
	require.NoError(t, err)

	// bash should not exist in the filtered registry.
	_, err = filtered.Execute(context.Background(), "bash", json.RawMessage(`{"command":"echo x"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool")
}

// TestAgentWithLiveProvider runs against the real LLM provider and records
// fixture files. Set RECORD=true and TEST_LLM_* env vars to enable.
func TestAgentWithLiveProvider(t *testing.T) {
	if os.Getenv("RECORD") != "true" {
		t.Skip("set RECORD=true and TEST_LLM_* to run against the real provider")
	}
	baseURL, apiKey, model := liveCredentials()
	if baseURL == "" || apiKey == "" {
		t.Fatal("TEST_LLM_BASE_URL and TEST_LLM_API_KEY must be set")
	}

	fixturePath := filepath.Join("testdata", "fixtures", "live_simple_chat.json")
	ft := aicli.NewFixtureTransport(fixturePath, http.DefaultTransport)

	client := aicli.NewClient(baseURL, apiKey, model)
	client.HTTPClient = &http.Client{Transport: ft}

	reg := aicli.NewRegistry()
	agent := aicli.New(aicli.Config{
		Client:       client,
		Registry:     reg,
		ProviderName: "live-provider",
		AgentName:    "test-agent",
	})

	result, err := agent.Run(context.Background(), "", "Reply with just the number 42, nothing else.")
	require.NoError(t, err)
	t.Logf("Live provider response: %q", result)
	assert.True(t, strings.Contains(result, "42"), "expected response to contain 42, got: %s", result)
}
