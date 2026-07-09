package hindsight

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"agent-orchestrator/db"
)

// LLMConfig is the model Hindsight uses for one operation (retain,
// consolidation, or reflect). It is resolved from the app's "Default Models"
// settings; BaseURL points at this server's own OpenAI-compatible LLM
// gateway (/api/proxy/provider/... or /api/proxy/group/...), so hindsight
// traffic gets proxy logging, token stats, and group failover.
type LLMConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// OpLLMConfigs bundles the per-operation LLM configs passed to hindsight-api.
// Retain also doubles as the base/fallback LLM: hindsight-api falls back to
// it for any operation whose own override isn't set. Consolidation and
// Reflect are optional overrides only sent when explicitly configured.
type OpLLMConfigs struct {
	Retain           LLMConfig
	HasRetain        bool
	Consolidation    LLMConfig
	HasConsolidation bool
	Reflect          LLMConfig
	HasReflect       bool
}

// Manager owns the bare-metal hindsight-api process. When HINDSIGHT_API_URL
// is set (external server, e2e mock) no process is spawned — the URL is used
// as-is. Otherwise the hindsight-api binary installed by the setup script
// into the app venv is launched with its embedded PostgreSQL storage.
type Manager struct {
	mu      sync.RWMutex
	client  *Client
	cmd     *exec.Cmd
	cmdDone chan struct{} // closed when the current cmd's Wait returns
	llm     func(ctx context.Context) OpLLMConfigs
	baseURL string
	port    string

	// startMu serializes Start so concurrent callers (startup goroutine plus
	// a settings-change re-trigger) can never spawn two processes.
	startMu sync.Mutex
}

func NewManager(llm func(ctx context.Context) OpLLMConfigs) *Manager {
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
	m.startMu.Lock()
	defer m.startMu.Unlock()
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

	// PDEATHSIG only exists on Linux; on macOS a crashed orchestrator leaves
	// hindsight-api running with stale config, holding the port. Stop such
	// leftovers before spawning fresh.
	reclaimOrphans(m.port)

	env := os.Environ()
	cfgs := m.llm(ctx)
	if cfgs.HasRetain {
		env = append(env,
			"HINDSIGHT_API_LLM_PROVIDER=openai",
			"HINDSIGHT_API_LLM_BASE_URL="+cfgs.Retain.BaseURL,
			"HINDSIGHT_API_LLM_API_KEY="+cfgs.Retain.APIKey,
			"HINDSIGHT_API_LLM_MODEL="+cfgs.Retain.Model,
		)
	} else {
		log.Println("hindsight: no retain model configured — starting without an LLM (retain quality degraded)")
		env = append(env, "HINDSIGHT_API_LLM_PROVIDER=none")
	}
	if cfgs.HasConsolidation {
		env = append(env,
			"HINDSIGHT_API_CONSOLIDATION_LLM_PROVIDER=openai",
			"HINDSIGHT_API_CONSOLIDATION_LLM_BASE_URL="+cfgs.Consolidation.BaseURL,
			"HINDSIGHT_API_CONSOLIDATION_LLM_API_KEY="+cfgs.Consolidation.APIKey,
			"HINDSIGHT_API_CONSOLIDATION_LLM_MODEL="+cfgs.Consolidation.Model,
		)
	}
	if cfgs.HasReflect {
		env = append(env,
			"HINDSIGHT_API_REFLECT_LLM_PROVIDER=openai",
			"HINDSIGHT_API_REFLECT_LLM_BASE_URL="+cfgs.Reflect.BaseURL,
			"HINDSIGHT_API_REFLECT_LLM_API_KEY="+cfgs.Reflect.APIKey,
			"HINDSIGHT_API_REFLECT_LLM_MODEL="+cfgs.Reflect.Model,
		)
	}
	env = append(env,
		// Memory export/import rides the app backup path.
		"HINDSIGHT_API_ENABLE_DOCUMENT_EXPORT_API=true",
		"HINDSIGHT_API_ENABLE_DOCUMENT_IMPORT_API=true",
		// Stable worker id so background jobs survive restarts.
		"HINDSIGHT_API_WORKER_ID=paperclip2",
	)
	if runtime.GOOS == "darwin" {
		// Torch MPS crashes (SIGSEGV, pointer-authentication failures) when
		// driven from worker threads in a daemon child process — Hindsight's
		// own force-CPU knobs exist for exactly this. The local models
		// (bge-small, MiniLM) are small enough that CPU inference is fine.
		env = append(env,
			"HINDSIGHT_API_EMBEDDINGS_LOCAL_FORCE_CPU=true",
			"HINDSIGHT_API_RERANKER_LOCAL_FORCE_CPU=true",
		)
	}

	cmd := exec.Command(bin, "--port", m.port, "--host", "127.0.0.1")
	cmd.Env = env
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	setProcAttrs(cmd) // on Linux: die with the parent, never orphan embedded Postgres
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start hindsight-api: %w", err)
	}
	writePIDFile(cmd.Process.Pid)
	m.mu.Lock()
	m.cmd = cmd
	m.mu.Unlock()
	done := make(chan struct{})
	m.mu.Lock()
	m.cmdDone = done
	m.mu.Unlock()
	go func() {
		defer close(done)
		if werr := cmd.Wait(); werr != nil {
			log.Printf("hindsight-api exited: %v", werr)
		}
		m.mu.Lock()
		// Only clear state we still own — a Stop+Start cycle may have
		// already replaced cmd/client with a newer process.
		if m.cmd == cmd {
			m.client = nil
			m.cmd = nil
			removePIDFile()
		}
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

// Stop terminates a locally-spawned hindsight-api process, if any. It asks
// politely first (SIGTERM) so the embedded Postgres can shut down cleanly,
// and only escalates to SIGKILL after a grace period.
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd, done := m.cmd, m.cmdDone
	m.cmd = nil
	m.client = nil
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	defer removePIDFile()
	_ = cmd.Process.Signal(os.Interrupt)
	if done != nil {
		select {
		case <-done:
			return
		case <-time.After(10 * time.Second):
		}
	}
	_ = cmd.Process.Kill()
}
