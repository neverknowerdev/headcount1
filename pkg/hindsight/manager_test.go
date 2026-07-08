package hindsight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestManagerAdoptsExternalURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("HINDSIGHT_API_URL", srv.URL)

	m := NewManager(func(ctx context.Context) (LLMConfig, bool) { return LLMConfig{}, false })
	if m.Client() != nil {
		t.Fatal("client must be nil before Start")
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if m.Client() == nil {
		t.Fatal("expected a client after adopting the external URL")
	}
	if m.BaseURL() != srv.URL {
		t.Errorf("expected base url %s, got %s", srv.URL, m.BaseURL())
	}

	// Second Start is a no-op when already healthy.
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("re-Start: %v", err)
	}

	// Stop with no spawned process must not panic and clears the client.
	m.Stop()
	if m.Client() != nil {
		t.Fatal("expected nil client after Stop")
	}
}

func TestManagerStartRespectsContextCancel(t *testing.T) {
	// Point at a dead address so health never succeeds.
	t.Setenv("HINDSIGHT_API_URL", "http://127.0.0.1:1")

	m := NewManager(func(ctx context.Context) (LLMConfig, bool) { return LLMConfig{}, false })
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if err := m.Start(ctx); err == nil {
		t.Fatal("expected an error when context is cancelled")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("Start did not return promptly after cancellation")
	}
}
