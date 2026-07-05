package hindsight

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"agent-orchestrator/db"
)

// LLMConfig is the model Hindsight uses for fact extraction / reflection.
// It is resolved from the app's cheap "utility model" settings: the provider's
// BaseUrl acts as an OpenAI-compatible proxy URL for Hindsight.
type LLMConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Manager owns the bare-metal hindsight-api process. When HINDSIGHT_API_URL
// is set (external server, e2e mock) no process is spawned — the URL is used
// as-is. Otherwise the hindsight-api binary installed by the setup script
// into the app venv is launched with its embedded PostgreSQL storage.
type Manager struct {
	mu      sync.RWMutex
	client  *Client
	cmd     *exec.Cmd
	llm     func(ctx context.Context) (LLMConfig, bool)
	baseURL string
	port    string
}

func NewManager(llm func(ctx context.Context) (LLMConfig, bool)) *Manager {
	port := os.Getenv("HINDSIGHT_PORT")
	if port == "" {
		port = "8888"
	}
	return &Manager{llm: llm, port: port}
}

// Client returns the API client once the backend is healthy, else nil.
func (m *Manager) Client() *Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.client
}

func (m *Manager) BaseURL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.baseURL
}

// binaryPath returns the hindsight-api executable inside the app venv.
func binaryPath() string {
	return filepath.Join(db.PaperclipHome(), "venv", "bin", "hindsight-api")
}

// Start brings the memory backend up. Blocking; intended to run in a
// background goroutine at startup. Safe to call again after a settings
// change (it is a no-op when already healthy).
func (m *Manager) Start(ctx context.Context) error {
	if m.Client() != nil {
		return nil
	}

	if url := os.Getenv("HINDSIGHT_API_URL"); url != "" {
		return m.adopt(ctx, url, 30*time.Second)
	}

	bin := binaryPath()
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("hindsight-api not installed (%s): run setup first", bin)
	}

	env := os.Environ()
	if cfg, ok := m.llm(ctx); ok {
		env = append(env,
			"HINDSIGHT_API_LLM_PROVIDER=openai",
			"HINDSIGHT_API_LLM_BASE_URL="+cfg.BaseURL,
			"HINDSIGHT_API_LLM_API_KEY="+cfg.APIKey,
			"HINDSIGHT_API_LLM_MODEL="+cfg.Model,
		)
	} else {
		log.Println("hindsight: no utility model configured — starting without an LLM (retain quality degraded)")
		env = append(env, "HINDSIGHT_API_LLM_PROVIDER=none")
	}
	env = append(env,
		// Memory export/import rides the app backup path.
		"HINDSIGHT_API_ENABLE_DOCUMENT_EXPORT_API=true",
		"HINDSIGHT_API_ENABLE_DOCUMENT_IMPORT_API=true",
		// Stable worker id so background jobs survive restarts.
		"HINDSIGHT_API_WORKER_ID=paperclip2",
	)

	cmd := exec.Command(bin, "--port", m.port, "--host", "127.0.0.1")
	cmd.Env = env
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start hindsight-api: %w", err)
	}
	m.mu.Lock()
	m.cmd = cmd
	m.mu.Unlock()
	go func() {
		if werr := cmd.Wait(); werr != nil {
			log.Printf("hindsight-api exited: %v", werr)
		}
		m.mu.Lock()
		m.client = nil
		m.cmd = nil
		m.mu.Unlock()
	}()

	// First boot downloads models and initializes embedded Postgres — allow
	// a generous window before declaring failure.
	return m.adopt(ctx, "http://127.0.0.1:"+m.port, 5*time.Minute)
}

// adopt polls url's health endpoint until it responds, then publishes the client.
func (m *Manager) adopt(ctx context.Context, url string, timeout time.Duration) error {
	c := NewClient(url)
	deadline := time.Now().Add(timeout)
	for {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.Health(hctx)
		cancel()
		if err == nil {
			m.mu.Lock()
			m.client = c
			m.baseURL = url
			m.mu.Unlock()
			log.Printf("hindsight: memory backend ready at %s", url)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hindsight at %s not healthy after %s: %w", url, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Stop terminates a locally-spawned hindsight-api process, if any.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
	m.cmd = nil
	m.client = nil
}
