package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/integration"
	"agent-orchestrator/pkg/appsettings"
	"agent-orchestrator/pkg/backup"
	"agent-orchestrator/pkg/bootkey"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/llmdiscovery"
	"agent-orchestrator/pkg/mailer"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"
	"agent-orchestrator/pkg/setup"
	"agent-orchestrator/pkg/updater"
	"agent-orchestrator/pkg/utils"
	"agent-orchestrator/server"
	endpoints "agent-orchestrator/server/controllers"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// These are set at build time via -ldflags.
var (
	CommitHash = "unknown"
	BuildDate  = "unknown"
	Branch     = "main"
)

// runDrainTimeout bounds how long a graceful shutdown waits for active agent
// runs to reach a pausable turn boundary (see NativeEngine.BeginDrain) before
// giving up and proceeding anyway. Generous enough to cover a slow LLM
// response; a run wedged in a long tool call won't fit in this window
// regardless of size, so there's little value in waiting much longer.
const runDrainTimeout = 5 * time.Minute

//go:embed all:frontend/dist
var frontendDist embed.FS

func main() {
	// Must run before anything else: when this process is a sandbox re-exec
	// child (Linux Landlock, see engine/aicli/tools), this applies the
	// filesystem ruleset and execs the shell command in place of the server.
	tools.MaybeRunSandboxChild()

	settings := appsettings.Load()
	basePath := settings.BasePath

	// Create the base directory tree (db/, ssh/, uploads/, repos/, ...) so
	// every subsystem can rely on its root existing.
	if err := filesystem.NewManager(basePath).SetupBaseDirectories(); err != nil {
		log.Printf("Warning: failed to create base directories: %v", err)
	}

	// Confine the agent's shell to its own task. The kernel sandbox (Landlock /
	// Seatbelt) hides the entire headcount1 data root from the untrusted shell,
	// re-granting only the task's own dirs per run (workspace, parent workspace,
	// project repo, artifacts). So the DB, SSH keys, credentials, backups, the
	// keyring snapshot, and every OTHER company's/task's files are all invisible,
	// while system and home toolchains stay readable. See doc/sandbox-hardening.md.
	tools.SetHiddenReadDirs([]string{basePath})

	dbConnStr := os.Getenv("DATABASE_URL")

	var database *gorm.DB
	var err error

	if strings.HasPrefix(dbConnStr, "postgres://") {
		log.Println("Connecting to PostgreSQL database")
		database, err = gorm.Open(postgres.Open(dbConnStr), &gorm.Config{})
	} else {
		log.Println("Connecting to SQLite database")
		if dbConnStr == "" {
			// The SQLite file lives under BasePath so every worktree process
			// pointed at the same home shares one database (WAL handles the
			// cross-process concurrency).
			fileName := "headcount1.db"
			if utils.IsE2E() {
				fileName = "headcount1-e2e.db"
			}
			dbDir := filepath.Join(basePath, "db")
			if err := os.MkdirAll(dbDir, 0755); err != nil {
				log.Fatalf("Failed to create database directory %s: %v", dbDir, err)
			}
			dbConnStr = filepath.Join(dbDir, fileName)
		}
		dsn := dbConnStr + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
		database, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	}

	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// Single in-process connection avoids GORM+SQLite lock churn; WAL covers
	// concurrency between processes.
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)

	log.Println("Running AutoMigrate...")
	err = database.AutoMigrate(
		&db.User{},
		&db.WebAuthnCredential{},
		&db.WebAuthnSession{},
		&db.Team{},
		&db.TeamMember{},
		&db.TeamInvite{},
		&db.Session{},
		&db.RefreshToken{},
		&db.UserGitCredential{},
		&db.PasswordResetToken{},
		&db.Company{},
		&db.Project{},
		&db.Sprint{},
		&db.LLMProvider{},
		&db.ModelGroup{},
		&db.ModelGroupMember{},
		&db.ModelRequestStat{},
		&db.DefaultModelSetting{},
		&db.ProviderPreset{},
		&db.Agent{},
		&db.Skill{},
		&db.Task{},
		&db.Comment{},
		&db.Attachment{},
		&db.Run{},
		&db.Artifact{},
		&db.ActivityLog{},
		&db.ProxyRequestLog{},
		&db.MCPServer{},
		&db.MCPAccount{},
		&db.AgentMCPServer{},
		&db.AgentMCPAccount{},
		&db.MCPToolStat{},
		&db.AgentMCPToolFilter{},
	)
	if err != nil {
		log.Fatalf("AutoMigrate failed: %v", err)
	}

	recoverStaleRuns(database)

	// Seed predefined MCP servers (headcount1, github, google-docs) if not present.
	if err := db.New(database).EnsureBuiltinMCPServers(context.Background()); err != nil {
		log.Printf("Warning: failed to seed built-in MCP servers: %v", err)
	}

	// Builtin free-model providers (OpenRouter, OpenCode Zen) and the
	// "Default Models" purposes are per-user: seeded at registration and, for
	// every existing user, here at startup (covers upgrades adding new
	// builtins/purposes). Their model catalog is populated purely from a live
	// fetch in the background — no hardcoded model list.
	if users, err := db.New(database).ListUsers(context.Background()); err != nil {
		log.Printf("Warning: failed to list users for builtin seeding: %v", err)
	} else {
		for _, u := range users {
			if err := db.New(database).EnsureTeamForUser(context.Background(), u); err != nil {
				log.Printf("Warning: failed to ensure team for %s: %v", u.Email, err)
			}
			if err := db.New(database).EnsureBuiltinLLMProvidersForUser(context.Background(), u.ID); err != nil {
				log.Printf("Warning: failed to seed built-in LLM providers for %s: %v", u.Email, err)
			}
			if err := db.New(database).EnsureDefaultModelSettingsForUser(context.Background(), u.ID); err != nil {
				log.Printf("Warning: failed to seed default model settings for %s: %v", u.Email, err)
			}
		}
	}
	// Seed the known provider presets (OpenCode Go, MiniMax, ...) users can
	// pick from a dropdown when adding a provider. These are a global catalog;
	// they don't become actual LLMProvider rows until a user picks one and
	// supplies an API key.
	if err := db.New(database).EnsureProviderPresets(context.Background()); err != nil {
		log.Printf("Warning: failed to seed provider presets: %v", err)
	}
	go refreshBuiltinLLMProviderModels(database)
	go llmdiscovery.StartDailyModelRefreshScheduler(context.Background(), db.New(database), &http.Client{Timeout: 20 * time.Second})

	// Repair codegraph servers whose project_id was not set on creation.
	if err := db.New(database).RepairOrphanedCodegraphServers(context.Background()); err != nil {
		log.Printf("Warning: codegraph repair failed: %v", err)
	}

	// Add FK constraint from mcp_servers.project_id → projects.id (SQLite table rebuild).
	if err := db.New(database).MigrateAddProjectFKToMCPServers(context.Background()); err != nil {
		log.Printf("Warning: mcp_servers FK migration: %v", err)
	}

	// Backfill the provider domain slug for rows created before the column
	// existed, so tenant export/import can dedup providers by slug.
	if err := db.New(database).BackfillProviderSlugs(context.Background()); err != nil {
		log.Printf("Warning: provider slug backfill: %v", err)
	}

	// Secrets (provider API keys, MCP tokens, SSH keys) are sealed per-user under
	// keys derived from each user's passkey, held only in memory while they're
	// signed in; the app decrypts only at the point of use.
	log.Printf("Secrets encrypted at rest with per-user passkey-derived keys.")

	// Restore the graceful-exit keyring snapshot, if a boot key is configured
	// and a snapshot from a planned shutdown exists — so a deploy re-warms
	// active users' vaults without a passkey re-tap. The blob is deleted after
	// loading so an unexpected crash can never replay a stale keyring.
	bootKey := bootkey.FromEnv()
	// When no external boot key is configured, an operator can opt into a
	// self-managed local boot key (HEADCOUNT1_LOCAL_BOOTKEY): a random key held
	// in memory, written to disk only at graceful shutdown next to the snapshot
	// and consumed at the next boot — so nothing sits on disk during runtime.
	// For local/dev only (see doc/boot-key.md); production uses an external key.
	var localBootKey *bootkey.LocalBootKey
	if bootKey == nil && bootkey.LocalBootKeyEnabled() {
		if lk, err := bootkey.LoadOrCreateLocalBootKey(filepath.Join(basePath, "keyring.bootkey")); err != nil {
			log.Printf("Warning: could not initialize self-managed local boot key: %v", err)
		} else {
			localBootKey = lk
			bootKey = lk
		}
	}
	keyringBlobPath := filepath.Join(basePath, "keyring.sealed")
	if bootKey != nil {
		if blob, err := os.ReadFile(keyringBlobPath); err == nil {
			// Re-warm with the same ceiling a normal unlock grants (the absolute
			// session cap), not the longer SessionLifetime — a restored DEK must
			// not outlive the session that could authenticate its owner.
			if err := secrets.Default().UnsealKeyring(bootKey, blob, db.SessionAbsoluteCap()); err != nil {
				log.Printf("Warning: could not restore sealed keyring: %v", err)
			} else {
				log.Printf("Restored %d unlocked vault(s) from graceful-exit snapshot (boot key: %s)", secrets.DefaultKeyring().Len(), bootKey.Name())
			}
			// The snapshot must never survive boot: a lingering blob would let an
			// unexpected later crash replay a stale keyring. If the unlink fails,
			// truncate it so it can't be replayed, and refuse to continue if even
			// that fails rather than leave sealed DEKs on disk.
			if err := os.Remove(keyringBlobPath); err != nil && !os.IsNotExist(err) {
				log.Printf("Warning: could not delete keyring snapshot %s: %v — truncating", keyringBlobPath, err)
				if terr := os.Truncate(keyringBlobPath, 0); terr != nil {
					log.Fatalf("FATAL: keyring snapshot %s could be neither deleted nor truncated (%v); refusing to run with a replayable snapshot on disk", keyringBlobPath, terr)
				}
			}
		}
	}

	// Transactional mail (passkey recovery): SMTP_* env vars, or a logging
	// no-op mailer that prints the link to the server log.
	endpoints.SetMailer(mailer.FromEnv())

	hub := eventhub.NewHub()
	hub.SetCompanyRecipientsResolver(newCompanyRecipientsResolver(database))

	eng := engine.NewNativeEngine(database, hub)
	log.Println("Using native engine")

	// Pick back up any run a previous graceful shutdown (e.g. applying an
	// auto-update) paused mid-flight — see NativeEngine.BeginDrain. Runs in
	// the background so a large backlog never delays server startup.
	go eng.ResumeInterruptedRuns(context.Background())

	// Deploys are pushed to this server by CI via the authenticated
	// /api/deploy/webhook (see the deploy controller); the updater just applies
	// them (download the release-asset binary, self-replace, graceful restart).
	// The download token is only needed if the releases repo is private.
	upd := updater.New(Branch, CommitHash, BuildDate, utils.DeployDownloadToken)
	log.Printf("Deploy target: env=%s, build=%s", utils.DeployEnv(), upd.Current().DisplayString())

	srv := server.NewServer(database, eng)
	srv.SetHub(hub)
	srv.SetUpdater(upd)

	// Run setup script and npm installs in the background so the HTTP server starts immediately.
	go func() {
		if err := setup.Run(); err != nil {
			log.Printf("WARNING: startup setup failed — some features may be unavailable: %v", err)
		}
		srv.InstallMCPNpmDeps(context.Background())
		srv.CacheMCPTools(context.Background())
	}()
	go srv.StartMCPCacheScheduler(context.Background())

	// Resume codegraph init for any project whose knowledge graph isn't ready yet.
	go srv.InitPendingCodegraphServers(context.Background())

	// Check if backup is needed on startup. Backups operate on the configured
	// BasePath (not the raw Headcount1Home) so a custom storage location is
	// what actually gets backed up.
	if backup.ShouldBackupOnStartup(basePath) {
		log.Println("Latest backup is older than 24h, running backup on startup...")
		go func() {
			_, err := backup.CreateBackup(basePath, database)
			if err != nil {
				log.Printf("Startup backup failed: %v", err)
			}
		}()
	}
	go backup.StartDailyScheduler(basePath, database)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("pong"))
		})

		if utils.IsE2E() {
			log.Println("E2E mode enabled - e2e routes active")
			r.Route("/e2e", func(r chi.Router) {
				api := endpoints.NewAPI(database, eng, hub)
				r.Post("/wipe-db", api.WipeDB)
				r.Post("/register", api.E2ERegister)
				r.Post("/lock", api.E2ELock)
				r.Get("/reveal-provider/{id}", api.E2ERevealProviderSecret)
			})
		}

		// Public: auth endpoints + the setup-status probe polled pre-login.
		srv.MountPublic(r)

		// Machine-to-machine: the agent subprocess calls the local LLM proxy
		// with provider headers, not a user session — user auth must not gate
		// these routes or every agent run breaks.
		gw := integration.NewLLMGatewayWithHub(database, hub)
		// Per-run token enforcement: only live agent runs (engine-issued
		// tokens) or authenticated sessions may use the proxy.
		gw.SetRunTokenValidator(runtokens.Default().Validate)
		gw.Mount(r)

		// Everything else — the human-facing API, including /ws — requires a
		// logged-in user.
		r.Group(func(r chi.Router) {
			r.Use(srv.AuthMiddleware())
			r.Use(srv.CSRFMiddleware())
			srv.Mount(r)
		})
	})

	// Expired access sessions and spent/expired refresh tokens accumulate
	// silently; sweep them hourly.
	go func() {
		q := db.New(database)
		for {
			time.Sleep(time.Hour)
			if err := q.DeleteExpiredSessions(context.Background()); err != nil {
				log.Printf("session GC failed: %v", err)
			}
			if err := q.DeleteExpiredRefreshTokens(context.Background()); err != nil {
				log.Printf("refresh token GC failed: %v", err)
			}
			// Proactively drop unlocked DEKs whose TTL (the absolute session cap)
			// has lapsed, so a dead session's key never lingers in memory beyond
			// the point the user could still authenticate.
			secrets.DefaultKeyring().EvictExpired()
		}
	}()

	distFS, err := fs.Sub(frontendDist, "frontend/dist")
	if err != nil {
		log.Fatalf("Failed to create sub filesystem: %v", err)
	}

	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		// Disable caching for JS and CSS files
		if strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
		}

		fsHandler := http.FileServer(http.FS(distFS))

		path := r.URL.Path
		if path == "/" {
			fsHandler.ServeHTTP(w, r)
			return
		}

		f, err := distFS.Open(path[1:])
		if os.IsNotExist(err) {
			r.URL.Path = "/"
			fsHandler.ServeHTTP(w, r)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.Close()

		fsHandler.ServeHTTP(w, r)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	httpServer := &http.Server{Addr: ":" + port, Handler: r}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("Starting server on port %s", port)
		// Tolerate a briefly-busy port (see listenWithRetry) instead of dying on
		// the first bind attempt; a genuinely occupied port still fails once the
		// deadline passes.
		listener, err := listenWithRetry(httpServer.Addr, 10*time.Second)
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down…")

	// Stop accepting new agent runs and let every run still in flight reach
	// its next safe pause point (right after its current turn's LLM response
	// arrives) instead of continuing — see NativeEngine.BeginDrain. Paused
	// runs persist their conversation and resume automatically on the next
	// boot (ResumeInterruptedRuns, called above). Bounded: a run stuck inside
	// a long-running or blocking tool call (shell command, ask_human,
	// delegation, ...) won't reach a turn boundary in time and is abandoned
	// here — it's recovered the same way any ungraceful crash is, via the
	// ordinary stale-run cleanup on the next boot.
	eng.BeginDrain()
	// Logged AFTER BeginDrain so the line is proof that draining is already in
	// effect (new runs refused, active runs will pause at their next turn) —
	// the e2e drain/resume test gates on exactly this ordering.
	log.Println("Draining active agent runs...")
	drainCtx, drainCancel := context.WithTimeout(context.Background(), runDrainTimeout)
	eng.WaitForActiveRuns(drainCtx)
	drainCancel()

	// Seal the in-memory keyring under the boot key so this planned restart
	// re-warms without a passkey re-tap. Only written on a graceful exit — an
	// unexpected crash leaves nothing behind (strict zero-knowledge).
	if bootKey != nil {
		if blob, err := secrets.Default().SealKeyring(bootKey); err != nil {
			log.Printf("Warning: could not seal keyring on shutdown: %v", err)
		} else if len(blob) > 0 {
			if err := os.WriteFile(keyringBlobPath, blob, 0600); err != nil {
				log.Printf("Warning: could not persist sealed keyring: %v", err)
			} else if localBootKey != nil {
				// Self-managed mode: write the boot key to disk ONLY now, next to
				// the snapshot it seals. The next boot consumes and deletes both.
				if err := localBootKey.Persist(); err != nil {
					log.Printf("Warning: could not persist local boot key: %v", err)
					_ = os.Remove(keyringBlobPath) // don't leave a snapshot we can't reopen
				}
			}
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)

	// A deploy replaced our executable on disk (see updater.Deploy): exec it
	// now, as the very last thing this process does. Everything the new build
	// depends on has already happened — in-flight agent runs are drained and
	// persisted (so its resume scan finds them), the keyring is sealed, and the
	// listener is closed (so it can bind the port immediately). syscall.Exec
	// replaces this process image in place, keeping the same PID, so there is
	// never a window with two servers competing for the port.
	if execPath, pending := upd.RestartPending(); pending {
		log.Printf("Deploy: exec into new binary %s", execPath)
		if err := syscall.Exec(execPath, os.Args, os.Environ()); err != nil {
			// Exec only returns on failure; the old image is still running but
			// has already shut down its listener, so there is nothing to serve.
			log.Fatalf("deploy: exec into new binary failed: %v", err)
		}
	}
}

// listenWithRetry binds addr, retrying on "address already in use" until
// deadline elapses. A deploy restart no longer contends for the port (the
// outgoing process closes its listener and then execs in place, so only one
// server ever holds it), but an external restart — a process manager relaunching
// us, or a socket still winding down — can briefly find the port busy, and
// dying immediately on that is worse than waiting a moment.
func listenWithRetry(addr string, deadline time.Duration) (net.Listener, error) {
	giveUpAt := time.Now().Add(deadline)
	var lastErr error
	for {
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			return listener, nil
		}
		lastErr = err
		if time.Now().After(giveUpAt) {
			return nil, lastErr
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// newCompanyRecipientsResolver returns a TTL-cached company → member-users
// lookup for tenant-scoped WebSocket event delivery: the owning team's
// members (or, for team-less rows, the creating user). A short TTL keeps
// newly invited members receiving events promptly.
func newCompanyRecipientsResolver(database *gorm.DB) func(companyID int32) ([]int32, bool) {
	type entry struct {
		users []int32
		ok    bool
		at    time.Time
	}
	var mu sync.Mutex
	cache := map[int32]entry{}
	const ttl = 30 * time.Second
	q := db.New(database)
	return func(companyID int32) ([]int32, bool) {
		mu.Lock()
		e, hit := cache[companyID]
		mu.Unlock()
		if hit && time.Since(e.at) < ttl {
			return e.users, e.ok
		}
		var users []int32
		ok := false
		if company, err := q.GetCompany(context.Background(), companyID); err == nil {
			switch {
			case company.TeamID != nil:
				if ids, err := q.ListTeamUserIDs(context.Background(), *company.TeamID); err == nil && len(ids) > 0 {
					users, ok = ids, true
				}
			case company.UserID != nil:
				users, ok = []int32{*company.UserID}, true
			}
		}
		mu.Lock()
		cache[companyID] = entry{users: users, ok: ok, at: time.Now()}
		mu.Unlock()
		return users, ok
	}
}

func recoverStaleRuns(database *gorm.DB) {
	q := db.New(database)
	ctx := context.Background()

	staleRuns, err := q.GetStaleRunningRuns(ctx, 10*time.Minute)
	if err != nil {
		log.Printf("Warning: failed to check for stale runs: %v", err)
		return
	}

	if len(staleRuns) == 0 {
		return
	}

	log.Printf("Recovering %d stale run(s)...", len(staleRuns))
	for _, run := range staleRuns {
		log.Printf("Marking run %d (task %d) as failed due to inactivity", run.ID, run.TaskID)
		_ = q.UpdateRunLog(ctx, run.ID, "Run marked as failed: server restarted while run was in progress", "failed")
		_ = q.UnlockTaskRun(ctx, run.TaskID)
	}
}

// refreshBuiltinLLMProviderModels fetches the current free-model catalog for
// the built-in OpenRouter/OpenCode Zen providers. Runs in a goroutine so a
// slow or unreachable host never delays server startup; RefreshBuiltinProviderModels
// itself retries transient failures and falls back to a curated model list,
// so this always leaves the providers usable.
func refreshBuiltinLLMProviderModels(database *gorm.DB) {
	ctx := context.Background()
	q := db.New(database)
	client := &http.Client{Timeout: 20 * time.Second}

	if err := llmdiscovery.RefreshBuiltinProviderModels(ctx, q, client); err != nil {
		log.Printf("Warning: %v", err)
	}
}
