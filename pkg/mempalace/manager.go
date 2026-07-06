package mempalace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/mcp"
	"agent-orchestrator/pkg/setup"
)

// The manager holds package-level state (mirroring pkg/setup): one cached MCP
// client and one write lock per company palace, shared by the engine, the
// agent tool proxy and the /api/memory handlers so all writers serialize on
// the same mutex.

var (
	clientsMu sync.Mutex
	clients   = map[string]mcp.Client{} // server name → live stdio client

	companyLocks sync.Map // server name → *sync.Mutex

	wakeUpCache sync.Map // palacePath+wing → wakeUpEntry

	// activeRuns counts in-flight runs per palace path. While a palace has
	// active runs its wake-up cache TTL drops so parallel sibling tasks see
	// facts written by each other within minutes, not an hour.
	activeRunsMu sync.Mutex
	activeRuns   = map[string]int{}
)

// Wake-up cache TTLs: long when the company is idle, short while runs are in
// flight so sibling runs pick up fresh facts. Never go below activeWakeUpTTL —
// wake-up shells out to the CLI.
const (
	idleWakeUpTTL   = time.Hour
	activeWakeUpTTL = 5 * time.Minute
)

type wakeUpEntry struct {
	text    string
	fetched time.Time
}

// Available reports whether the mempalace runtime was installed by the setup
// script. Non-blocking: while setup is still running this returns false and
// memory features stay off for the request — they appear once setup finishes.
func Available() bool {
	return setup.MempalaceReadyNonBlocking()
}

// MemoryBasePath is the root directory holding all company palaces.
func MemoryBasePath() string {
	return filepath.Join(db.PaperclipHome(), "memory")
}

// PalacePath returns the palace directory for a company.
func PalacePath(company db.Company) string {
	return filepath.Join(MemoryBasePath(), SanitizeName(company.ShortName))
}

// ServerName returns the (unique) MCPServer row name for a company's palace.
func ServerName(company db.Company) string {
	return "mempalace-" + SanitizeName(company.ShortName)
}

// EnsureCompanyServer creates the palace directory and seeds (or refreshes)
// the company's mempalace MCPServer row. Safe to call repeatedly — it is the
// startup-sweep and company-creation hook. Tenant isolation comes from the
// per-company palace path baked into the server args.
func EnsureCompanyServer(ctx context.Context, q *db.Queries, company db.Company) (db.MCPServer, error) {
	if !Available() {
		return db.MCPServer{}, fmt.Errorf("mempalace is not installed")
	}
	palace := PalacePath(company)
	if err := os.MkdirAll(palace, 0755); err != nil {
		return db.MCPServer{}, fmt.Errorf("create palace dir: %w", err)
	}
	// Chunking settings live in the shared mempalace user config — keep them
	// in sync on every provisioning pass (they only affect new writes).
	if err := ensureChunkConfig(); err != nil {
		log.Printf("mempalace: could not write chunk config: %v", err)
	}

	argsJSON, _ := json.Marshal([]string{"--palace", palace})
	want := db.MCPServer{
		Name:        ServerName(company),
		DisplayName: fmt.Sprintf("Memory: %s", company.Name),
		Description: fmt.Sprintf("MemPalace memory palace for company %q — verbatim agent memory with semantic recall.", company.Name),
		Transport:   "stdio",
		Command:     setup.MempalaceMCPPath(),
		Args:        string(argsJSON),
		Builtin:     true,
		Enabled:     true,
		InitStatus:  "ready",
	}

	existing, err := q.GetMCPServerByName(ctx, want.Name)
	if err != nil {
		created, cErr := q.CreateMCPServer(ctx, want)
		if cErr != nil {
			return db.MCPServer{}, fmt.Errorf("seed mempalace server: %w", cErr)
		}
		log.Printf("mempalace: provisioned palace for company %q at %s", company.ShortName, palace)
		return created, nil
	}

	// Refresh mutable fields (venv path or palace layout may change between versions).
	if existing.Command != want.Command || existing.Args != want.Args || existing.InitStatus != "ready" || !existing.Enabled {
		existing.Command = want.Command
		existing.Args = want.Args
		existing.InitStatus = "ready"
		existing.Enabled = true
		if updated, uErr := q.UpdateMCPServer(ctx, existing); uErr == nil {
			existing = updated
		}
	}
	return existing, nil
}

// ServerForCompany returns the company's mempalace MCPServer row.
func ServerForCompany(ctx context.Context, q *db.Queries, company db.Company) (db.MCPServer, error) {
	return q.GetMCPServerByName(ctx, ServerName(company))
}

// Ready reports whether a mempalace server row is usable.
func Ready(server db.MCPServer) bool {
	return server.Enabled && server.InitStatus == "ready"
}

// LockCompany takes the palace write lock for the given server and returns
// the unlock function. Every write path (agent tools, engine hooks, API
// mutations, CLI maintenance, backup) holds this — ChromaDB is effectively
// single-writer, so writes must serialize per palace.
func LockCompany(serverName string) func() {
	v, _ := companyLocks.LoadOrStore(serverName, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// writeTools is the set of mempalace MCP tools that mutate the palace.
var writeTools = map[string]bool{
	"mempalace_add_drawer":       true,
	"mempalace_update_drawer":    true,
	"mempalace_delete_drawer":    true,
	"mempalace_delete_by_source": true,
	"mempalace_kg_add":           true,
	"mempalace_kg_invalidate":    true,
	"mempalace_diary_write":      true,
	"mempalace_create_tunnel":    true,
	"mempalace_delete_tunnel":    true,
	"mempalace_delete_hallway":   true,
	"mempalace_mine":             true,
	"mempalace_sync":             true,
	"mempalace_checkpoint":       true,
}

// IsWriteTool reports whether the named mempalace MCP tool mutates the palace.
func IsWriteTool(tool string) bool { return writeTools[tool] }

// CallServerTool calls one mempalace MCP tool through the shared cached
// client for the server. Write tools are serialized under the company write
// lock. On a transport-level failure the cached client is dropped and the
// call retried once with a fresh subprocess, so a crashed server never
// permanently disables memory.
func CallServerTool(ctx context.Context, server db.MCPServer, tool string, args any) (string, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("marshal args: %w", err)
	}

	if IsWriteTool(tool) {
		unlock := LockCompany(server.Name)
		defer unlock()
	}

	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	client, err := clientFor(callCtx, server)
	if err != nil {
		return "", err
	}
	result, err := client.CallTool(callCtx, tool, argsJSON)
	if err == nil {
		return result, nil
	}

	// Tool-level errors (isError content) come back as normal errors too; only
	// retry when the transport itself looks dead.
	if !isTransportError(err) {
		return "", err
	}
	dropClient(server.Name)
	client, cErr := clientFor(callCtx, server)
	if cErr != nil {
		return "", err
	}
	return client.CallTool(callCtx, tool, argsJSON)
}

func isTransportError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"broken pipe", "closed", "eof", "process", "deadline exceeded", "context canceled"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func clientFor(ctx context.Context, server db.MCPServer) (mcp.Client, error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[server.Name]; ok {
		return c, nil
	}
	c, err := mcp.NewClient(server)
	if err != nil {
		return nil, fmt.Errorf("mempalace: start server %q: %w", server.Name, err)
	}
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := c.Initialize(initCtx); err != nil {
		c.Close()
		return nil, fmt.Errorf("mempalace: initialize server %q: %w", server.Name, err)
	}
	clients[server.Name] = c
	return c, nil
}

func dropClient(serverName string) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[serverName]; ok {
		c.Close()
		delete(clients, serverName)
	}
}

// CloseAll stops every cached mempalace server subprocess (tests/shutdown).
func CloseAll() {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	for name, c := range clients {
		c.Close()
		delete(clients, name)
	}
}

// InitProjectAsync runs `mempalace init` for a project workspace in a
// background goroutine, then chains into MineProjectAsync on success. Init
// only writes mempalace.yaml (+ entity registry) into workspaceDir — it never
// touches the chroma palace itself, so unlike `mine` it is safe to run as a
// plain CLI subprocess alongside the long-lived mempalace-mcp process without
// fighting over the palace writer lease (see MineProjectAsync's comment).
// --yes/--no-llm/--auto-mine=false keep it non-interactive and local-only;
// mining is deliberately left to the existing MCP-backed MineProjectAsync
// path so both go through their respective safe channels.
func InitProjectAsync(q *db.Queries, server db.MCPServer, company db.Company, project db.Project, workspaceDir string) {
	if !Available() || workspaceDir == "" {
		return
	}
	go func() {
		if _, err := os.Stat(workspaceDir); err != nil {
			log.Printf("mempalace: init skipped for project %q, workspace not on disk yet: %v", project.Name, err)
			return
		}
		log.Printf("mempalace: running init for project %q...", project.Name)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, setup.MempalaceCLIPath(),
			"--palace", PalacePath(company), "init", workspaceDir, "--yes", "--no-llm")
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("mempalace: init failed for project %q: %v — %s", project.Name, err, tail(string(out), 500))
			return
		}
		if err := q.SetMempalaceInitDone(context.Background(), project.ID, true); err != nil {
			log.Printf("mempalace: init succeeded for project %q but failed to persist flag: %v", project.Name, err)
		}
		log.Printf("mempalace: init completed for project %q", project.Name)
		MineProjectAsync(server, company, project, workspaceDir)
	}()
}

// MineProjectAsync indexes a project workspace into the company palace in a
// background goroutine (codegraph-init pattern). Failures are logged only —
// mining is an enrichment, not a dependency of memory availability.
//
// Goes through the shared MCP client (CallServerTool), NOT a CLI subprocess.
// The long-lived mempalace-mcp process holds the palace's writer lease for as
// long as memory is in use — a competing CLI `mine` process trying to grab
// its own writer lease on the same palace directory gets rejected (and, worse,
// can force the MCP server to demote itself to read-only for the duration:
// "Peer MCP writer active; this server is read-only for mutating tools"),
// which was silently dropping/delaying concurrent agent writes (remember(),
// diary, KG facts) during exactly this project-creation-time mining window.
// This mirrors MineTranscript's existing (correct) pattern — see its comment
// for the "palace is held by PID" failure mode this avoids.
func MineProjectAsync(server db.MCPServer, company db.Company, project db.Project, workspaceDir string) {
	if !Available() || workspaceDir == "" {
		return
	}
	go func() {
		if _, err := os.Stat(workspaceDir); err != nil {
			return
		}
		wing := ProjectWing(project.Name)
		log.Printf("mempalace: mining project %q into wing %q...", project.Name, wing)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		out, err := CallServerTool(ctx, server, "mempalace_mine", map[string]any{
			"source": workspaceDir, "mode": "projects", "wing": wing, "agent": "system",
		})
		if err != nil {
			log.Printf("mempalace: mine failed for project %q: %v — %s", project.Name, err, tail(out, 500))
			return
		}
		var parsed struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(out), &parsed) == nil && parsed.Error != "" {
			log.Printf("mempalace: mine failed for project %q: %s", project.Name, tail(parsed.Error, 500))
			return
		}
		log.Printf("mempalace: mine completed for project %q", project.Name)
	}()
}

// MineTranscript indexes a directory of purpose-built transcript JSONL files
// (see engine/aicli's transcript writer) into the company palace with the
// conversation miner. It goes through the shared MCP client — the long-lived
// mempalace-mcp process holds the palace writer lease, so a CLI `mine` would
// be rejected with "palace is held by PID" whenever memory is in use.
// Synchronous (CallServerTool serializes write tools under the company lock)
// and idempotent: the miner keeps per-file sentinels, so re-mining a dir only
// processes files whose content changed. Never pass raw session logs here —
// only transcript JSONL.
func MineTranscript(ctx context.Context, server db.MCPServer, wing, agentName, dir string) error {
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("transcript dir: %w", err)
	}
	out, err := CallServerTool(ctx, server, "mempalace_mine", map[string]any{
		"source": dir, "mode": "convos", "wing": wing, "agent": SanitizeName(agentName),
	})
	if err != nil {
		return fmt.Errorf("mempalace mine convos failed: %w", err)
	}
	// The tool reports failures as a structured error payload, not a
	// transport error — surface those too.
	var parsed struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(out), &parsed) == nil && parsed.Error != "" {
		return fmt.Errorf("mempalace mine convos failed: %s", parsed.Error)
	}
	return nil
}

// WakeUp returns the palace's L0/L1 wake-up context block for a wing, cached
// for an hour and hard-capped so it can never blow up the system prompt.
// Returns "" on any failure — the caller treats wake-up as optional.
func WakeUp(company db.Company, wing string) string {
	if !Available() {
		return ""
	}
	// The TTL is evaluated at read time so an entry cached while the company
	// was idle still refreshes quickly once runs go active.
	key := PalacePath(company) + "|" + wing
	if v, ok := wakeUpCache.Load(key); ok {
		if e := v.(wakeUpEntry); time.Since(e.fetched) < wakeUpTTL(PalacePath(company)) {
			return e.text
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, setup.MempalaceCLIPath(),
		"--palace", PalacePath(company), "wake-up", "--wing", wing)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(out))
	// ~1200 token cap (≈4 chars/token).
	const maxChars = 4800
	if len(text) > maxChars {
		text = text[:maxChars] + "\n[wake-up truncated]"
	}
	wakeUpCache.Store(key, wakeUpEntry{text: text, fetched: time.Now()})
	return text
}

// RunStarted marks a run as active for a company's palace, shortening the
// wake-up cache TTL so parallel sibling runs see each other's writes.
// Pair every call with RunEnded.
func RunStarted(company db.Company) {
	activeRunsMu.Lock()
	defer activeRunsMu.Unlock()
	activeRuns[PalacePath(company)]++
}

// RunEnded reverses RunStarted.
func RunEnded(company db.Company) {
	activeRunsMu.Lock()
	defer activeRunsMu.Unlock()
	palace := PalacePath(company)
	if activeRuns[palace] <= 1 {
		delete(activeRuns, palace)
	} else {
		activeRuns[palace]--
	}
}

// wakeUpTTL returns the cache TTL for a palace based on run activity.
func wakeUpTTL(palacePath string) time.Duration {
	activeRunsMu.Lock()
	defer activeRunsMu.Unlock()
	if activeRuns[palacePath] > 0 {
		return activeWakeUpTTL
	}
	return idleWakeUpTTL
}

// RepairPalace rebuilds a palace's vector index from its authoritative SQLite
// stores — used after a backup restore. Best-effort.
func RepairPalace(ctx context.Context, palaceDir string) error {
	cmd := exec.CommandContext(ctx, setup.MempalaceCLIPath(),
		"--palace", palaceDir, "repair")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mempalace repair failed for %s: %v — %s", palaceDir, err, tail(string(out), 300))
	}
	return nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
