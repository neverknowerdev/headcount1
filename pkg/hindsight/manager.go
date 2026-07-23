package hindsight

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// Status is the memory backend's health as served by /api/memory/status.
// Error is set when the backend failed to start or died — the frontend
// shows it as an app-wide banner. Notice carries a non-fatal degradation
// (e.g. memory was re-initialized on a fallback schema after an
// incompatible-migration crash) for the current process lifetime.
type Status struct {
	Available bool   `json:"available"`
	URL       string `json:"url,omitempty"`
	Schema    string `json:"schema,omitempty"`
	Error     string `json:"error,omitempty"`
	Notice    string `json:"notice,omitempty"`
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
	schema  string
	lastErr string
	notice  string
	tail    *tailBuffer // rolling output of the current/last spawned process

	// startMu serializes Start so concurrent callers (startup goroutine plus
	// a settings-change re-trigger) can never spawn two processes.
	startMu sync.Mutex

	// RecoverFromExport, when set (wired from main), restores memories into a
	// freshly initialized backend from the newest on-disk backup export via
	// Hindsight's own import API, and returns a user-facing summary. Called
	// after a schema fallback succeeds.
	RecoverFromExport func(ctx context.Context) string
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

// Status reports availability plus the last startup/runtime error and any
// degradation notice, for the app-wide memory banner.
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := Status{Schema: m.schema, Error: m.lastErr, Notice: m.notice}
	if m.client != nil {
		s.Available = true
		s.URL = m.baseURL
	}
	return s
}

func (m *Manager) setErr(msg string) {
	m.mu.Lock()
	m.lastErr = msg
	m.mu.Unlock()
}

func (m *Manager) setNotice(msg string) {
	m.mu.Lock()
	m.notice = msg
	m.mu.Unlock()
}

// binaryPath returns the hindsight-api executable inside the app venv.
func binaryPath() string {
	return filepath.Join(db.Headcount1Home(), "venv", "bin", "hindsight-api")
}

// Start brings the memory backend up. Blocking; intended to run in a
// background goroutine at startup. Safe to call again after a settings
// change (it is a no-op when already healthy).
//
// Startup failures never crash-loop silently: the failure is recorded for
// /api/memory/status, and a migration-history mismatch (the embedded
// Postgres was migrated by a different hindsight-api build, so Alembic
// cannot locate the DB's revision) triggers a one-time fallback to a fresh,
// version-scoped schema — the incompatible schema's data is left untouched.
func (m *Manager) Start(ctx context.Context) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if m.Client() != nil {
		return nil
	}

	if url := os.Getenv("HINDSIGHT_API_URL"); url != "" {
		err := m.adopt(ctx, url, 30*time.Second, nil)
		if err != nil {
			m.setErr(err.Error())
		} else {
			m.setErr("")
		}
		return err
	}

	bin := binaryPath()
	if _, statErr := os.Stat(bin); statErr != nil {
		err := fmt.Errorf("hindsight-api not installed (%s): run setup first", bin)
		m.setErr(err.Error())
		return err
	}

	// PDEATHSIG only exists on Linux; on macOS a crashed orchestrator leaves
	// hindsight-api running with stale config, holding the port. Stop such
	// leftovers before spawning fresh.
	reclaimOrphans(m.port)

	schemaOverridden := os.Getenv("HINDSIGHT_API_DATABASE_SCHEMA") != ""
	schema := resolveSchema()

	err := m.startWithSchema(ctx, schema)
	if err == nil {
		m.setErr("")
		return nil
	}

	// Migration-history mismatch: the schema's alembic_version points at a
	// revision the installed hindsight-api doesn't know (data was migrated
	// by a newer or custom build). Running the old code against the newer
	// schema is unsafe, so isolate: re-initialize memory on a fresh schema
	// named after the installed version. Deterministic naming means the
	// same install always lands on the same schema (no churn across
	// restarts) and a future incompatible version gets its own schema
	// instead of a crash loop. An explicit HINDSIGHT_API_DATABASE_SCHEMA
	// override is user intent — never second-guess it.
	if isMigrationFailure(m.lastTail()) && !schemaOverridden {
		version := installedVersion()
		fallback := fallbackSchemaName(version)
		if fallback != schema {
			log.Printf("hindsight: schema %q was migrated by an incompatible hindsight-api build — re-initializing memory on schema %q (installed hindsight-api %s); previous data is preserved untouched in %q",
				schema, fallback, versionOrUnknown(version), schema)
			if err2 := m.startWithSchema(ctx, fallback); err2 == nil {
				state := schemaState{
					Schema:           fallback,
					PreviousSchema:   schema,
					Reason:           fmt.Sprintf("schema %q has migrations from an incompatible hindsight-api build", schema),
					HindsightVersion: version,
					SwitchedAt:       time.Now().UTC().Format(time.RFC3339),
				}
				if serr := saveSchemaState(state); serr != nil {
					log.Printf("hindsight: warning: failed to persist schema state: %v", serr)
				}
				m.setNotice(m.recoveryNotice(ctx, schema, fallback))
				m.setErr("")
				return nil
			} else {
				err = err2
			}
		}
	}

	m.setErr(summarizeFailure(err.Error(), m.lastTail()))
	return err
}

// recoveryNotice restores memories into the fresh fallback schema and builds
// the user-facing notice. Recovery goes through Hindsight's own export/import
// API (the newest backup export on disk) rather than copying raw rows between
// schemas: across incompatible migrations a raw copy can silently corrupt
// data (changed column semantics, moved tables, a different embedding model
// or vector dimension), while import re-embeds facts and restores bank
// config/mental models against the schema actually in use. The old schema is
// never modified — whatever the export doesn't cover stays preserved there.
func (m *Manager) recoveryNotice(ctx context.Context, from, to string) string {
	notice := fmt.Sprintf(
		"Long-term memory was re-initialized on a fresh database schema (%q) because the previous schema (%q) was migrated by an incompatible hindsight-api version; the previous data remains untouched in %q. ",
		to, from, from)
	if m.RecoverFromExport == nil {
		return notice + "No recovery hook is configured, so previous memories were not carried over."
	}
	result := m.RecoverFromExport(ctx)
	log.Printf("hindsight: schema fallback recovery: %s", result)
	return notice + result
}

// startWithSchema spawns hindsight-api against one Postgres schema and waits
// for it to become healthy (or exit).
func (m *Manager) startWithSchema(ctx context.Context, schema string) error {
	env := m.buildEnv(ctx, schema)
	tail := newTailBuffer(32 << 10)
	out := io.MultiWriter(os.Stderr, tail)

	cmd := exec.Command(binaryPath(), "--port", m.port, "--host", "127.0.0.1")
	cmd.Env = env
	cmd.Stdout = out
	cmd.Stderr = out
	setProcAttrs(cmd) // on Linux: die with the parent, never orphan embedded Postgres
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start hindsight-api: %w", err)
	}
	writePIDFile(cmd.Process.Pid)
	done := make(chan struct{})
	m.mu.Lock()
	m.cmd = cmd
	m.cmdDone = done
	m.schema = schema
	m.tail = tail
	m.mu.Unlock()
	go func() {
		defer close(done)
		werr := cmd.Wait()
		if werr != nil {
			log.Printf("hindsight-api exited: %v", werr)
		}
		m.mu.Lock()
		// Only clear state we still own — a Stop+Start cycle may have
		// already replaced cmd/client with a newer process.
		if m.cmd == cmd {
			if m.client != nil {
				// Was healthy and died mid-flight: surface it app-wide.
				m.lastErr = fmt.Sprintf("memory backend (hindsight-api) exited unexpectedly: %v", werr)
			}
			m.client = nil
			m.cmd = nil
			removePIDFile()
		}
		m.mu.Unlock()
	}()

	// First boot downloads models and initializes embedded Postgres — allow
	// a generous window before declaring failure.
	return m.adopt(ctx, "http://127.0.0.1:"+m.port, 5*time.Minute, done)
}

// buildEnv assembles the child process environment: LLM routing through the
// app's own gateway, export/import for backups, and the storage schema.
func (m *Manager) buildEnv(ctx context.Context, schema string) []string {
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
		// Run migrations on startup so the base schema AND every already-created
		// per-team tenant schema (/v1/team-<id>/) are migrated/kept current. New
		// tenants materialize on first write (see Service.EnsureTenant).
		"HINDSIGHT_API_RUN_MIGRATIONS_ON_STARTUP=true",
		// Base ("default" tenant) storage schema — see resolveSchema/
		// fallbackSchemaName. This governs only the base schema now; each team's
		// memory lives in its own tenant schema selected by the request path.
		// Appended last so it wins over any inherited value.
		"HINDSIGHT_API_DATABASE_SCHEMA="+schema,
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
	return env
}

func (m *Manager) lastTail() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.tail == nil {
		return ""
	}
	return m.tail.String()
}

// adopt polls url's health endpoint until it responds, then publishes the
// client. procDead (nil for external URLs) aborts the wait as soon as the
// spawned process exits, so a startup crash fails in seconds instead of
// burning the whole timeout polling a dead port.
func (m *Manager) adopt(ctx context.Context, url string, timeout time.Duration, procDead <-chan struct{}) error {
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
		case <-procDead: // nil channel never fires
			return fmt.Errorf("hindsight-api exited during startup")
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

func versionOrUnknown(v string) string {
	if v == "" {
		return "(unknown version)"
	}
	return v
}

// tailBuffer is a thread-safe writer that keeps only the last max bytes —
// enough context to classify a startup failure without unbounded growth.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// isMigrationFailure reports whether captured hindsight-api output shows an
// Alembic migration failure — the signature of a DB whose migration history
// doesn't match the installed package.
func isMigrationFailure(output string) bool {
	for _, sig := range []string{
		"Database migration failed",
		"Can't locate revision identified by",
		"alembic.util.exc.CommandError",
		"alembic.script.revision.ResolutionError",
	} {
		if strings.Contains(output, sig) {
			return true
		}
	}
	return false
}

// summarizeFailure turns a startup error plus process output into one short
// human-readable line for the UI banner (the full output already went to the
// server log).
func summarizeFailure(errMsg, output string) string {
	// The most informative single line, if present.
	for _, line := range strings.Split(output, "\n") {
		if idx := strings.Index(line, "Can't locate revision identified by"); idx >= 0 {
			return "memory backend (hindsight-api) failed to start: database migration failed — " + strings.TrimSpace(line[idx:])
		}
	}
	if strings.Contains(output, "Database migration failed") {
		return "memory backend (hindsight-api) failed to start: database migration failed (see server log)"
	}
	msg := "memory backend (hindsight-api) failed to start: " + errMsg
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// installedVersion returns the hindsight-api package version installed in
// the app venv ("" when it can't be determined), read from the dist-info
// directory name to avoid spawning Python.
func installedVersion() string {
	pattern := filepath.Join(db.Headcount1Home(), "venv", "lib", "python*", "site-packages", "hindsight_api-*.dist-info")
	matches, _ := filepath.Glob(pattern)
	for _, match := range matches {
		base := filepath.Base(match)
		v := strings.TrimSuffix(strings.TrimPrefix(base, "hindsight_api-"), ".dist-info")
		if v != "" && v != base {
			return v
		}
	}
	return ""
}

// fallbackSchemaName derives the version-scoped schema used when the active
// schema's migrations are incompatible: mem_v0_6_1 for hindsight-api 0.6.1.
// Deterministic by design — the same installed version always maps to the
// same schema, so restarts reuse it instead of re-initializing again.
func fallbackSchemaName(version string) string {
	if version == "" {
		return "mem_fallback"
	}
	var b strings.Builder
	b.WriteString("mem_v")
	for _, r := range version {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
