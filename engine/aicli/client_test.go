package aicli_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"agent-orchestrator/engine/aicli"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRetryTestClient builds a Client pointed at srv with a fast retry base
// delay, so retry-loop tests run in milliseconds instead of real seconds.
func newRetryTestClient(srv *httptest.Server) *aicli.Client {
	c := aicli.NewClient(srv.URL, "test-key", "test-model")
	c.RetryBaseDelay = time.Millisecond
	return c
}

func okChatResponse() map[string]any {
	return map[string]any{
		"id":    "resp-1",
		"model": "test-model",
		"choices": []map[string]any{
			{"index": 0, "message": map[string]any{"role": "assistant", "content": "hello"}, "finish_reason": "stop"},
		},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
}

func TestComplete_RetriesOn5xxThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(okChatResponse())
	}))
	defer srv.Close()

	resp, _, err := newRetryTestClient(srv).Complete(context.Background(), aicli.ChatRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls))
	assert.Equal(t, "hello", resp.Choices[0].Message.Content)
}

func TestComplete_RetriesOn429ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(okChatResponse())
	}))
	defer srv.Close()

	_, _, err := newRetryTestClient(srv).Complete(context.Background(), aicli.ChatRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestComplete_DoesNotRetryOnHardClientError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	_, _, err := newRetryTestClient(srv).Complete(context.Background(), aicli.ChatRequest{})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "a hard 4xx must fail fast, not retry")
}

func TestComplete_NonJSONErrorIncludesStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("gateway requires a run token or an authenticated session"))
	}))
	defer srv.Close()

	_, _, err := newRetryTestClient(srv).Complete(context.Background(), aicli.ChatRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status=401")
	assert.Contains(t, err.Error(), "gateway requires a run token")
}

func TestComplete_RetriesOnNetworkError(t *testing.T) {
	// Simulate transient network failures (connection reset, DNS hiccup,
	// timeout) below the HTTP-status layer entirely, by hijacking and
	// dropping the connection instead of writing a response.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, err := hj.Hijack()
			require.NoError(t, err)
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(okChatResponse())
	}))
	defer srv.Close()

	_, _, err := newRetryTestClient(srv).Complete(context.Background(), aicli.ChatRequest{})
	require.NoError(t, err, "a transient network error should be retried automatically")
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls))
}

func TestComplete_RetriesOnEmbeddedRateLimitError(t *testing.T) {
	// Some OpenAI-compatible gateways (notably free-tier providers like
	// OpenRouter) report rate limiting via an "error" object in a 200 body
	// instead of (or in addition to) an HTTP 429 status.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 2 {
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "rate limit exceeded, retry later", "type": "rate_limit_exceeded", "code": "429"},
			})
			return
		}
		json.NewEncoder(w).Encode(okChatResponse())
	}))
	defer srv.Close()

	resp, _, err := newRetryTestClient(srv).Complete(context.Background(), aicli.ChatRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
	assert.Equal(t, "hello", resp.Choices[0].Message.Content)
}

func TestComplete_RetriesOnEmbeddedRateLimitError_NumericCode(t *testing.T) {
	// Some gateways send a numeric "code" (429) rather than a string one.
	// flexibleCode must decode this without blowing up the whole response.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 2 {
			w.Write([]byte(`{"error":{"message":"too many requests","type":"rate_limit","code":429}}`))
			return
		}
		json.NewEncoder(w).Encode(okChatResponse())
	}))
	defer srv.Close()

	_, _, err := newRetryTestClient(srv).Complete(context.Background(), aicli.ChatRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestComplete_DoesNotRetryOnEmbeddedHardError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "invalid API key", "type": "invalid_request_error", "code": "invalid_api_key"},
		})
	}))
	defer srv.Close()

	_, _, err := newRetryTestClient(srv).Complete(context.Background(), aicli.ChatRequest{})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "a hard embedded error must fail fast, not retry")
	assert.Contains(t, err.Error(), "invalid API key")
}

func TestComplete_ExhaustsRetriesAndReturnsError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newRetryTestClient(srv)
	_, _, err := c.Complete(context.Background(), aicli.ChatRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "all retries exhausted")
	// MaxRetries=3 (NewClient default) → 1 initial attempt + 3 retries = 4 calls.
	assert.Equal(t, int32(c.MaxRetries+1), atomic.LoadInt32(&calls))
}

func TestComplete_RespectsContextCancellationDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := aicli.NewClient(srv.URL, "test-key", "test-model")
	c.RetryBaseDelay = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := c.Complete(ctx, aicli.ChatRequest{})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, time.Second, "should abort during the backoff wait, not run all retries")
}

func TestComplete_ZeroValueClientDefaultsRetryDelay(t *testing.T) {
	// A Client built via a bare struct literal (RetryBaseDelay left at its
	// zero value) must still back off sanely instead of hammering the
	// provider with zero-delay retries.
	var calls int32
	var gotFirstCallAt, gotSecondCallAt time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			gotFirstCallAt = time.Now()
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		gotSecondCallAt = time.Now()
		json.NewEncoder(w).Encode(okChatResponse())
	}))
	defer srv.Close()

	c := &aicli.Client{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		Model:      "test-model",
		HTTPClient: srv.Client(),
		MaxRetries: 1,
		// RetryBaseDelay intentionally left unset.
	}
	_, _, err := c.Complete(context.Background(), aicli.ChatRequest{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, gotSecondCallAt.Sub(gotFirstCallAt), 900*time.Millisecond, "should fall back to the ~1s default backoff")
}
