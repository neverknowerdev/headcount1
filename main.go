package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
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
	"agent-orchestrator/pkg/hindsight"
	"agent-orchestrator/pkg/llmdiscovery"
	"agent-orchestrator/pkg/mailer"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"
	"agent-orchestrator/pkg/setup"
	"agent-orchestrator/pkg/utils"
	"agent-orchestrator/server"
	endpoints "agent-orchestrator/server/controllers"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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
		&db.HindsightDocument{},
		&db.SystemLLMLog{},
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

	srv := server.NewServer(database, eng)
	srv.SetHub(hub)

	// Hindsight long-term memory layer. The manager runs a bare-metal
	// hindsight-api process (installed into the app venv by the setup script)
	// whose LLMs come from the "Default Models" hindsight purposes, routed
	// through this server's own LLM gateway (see resolveHindsightLLMConfig);
	// HINDSIGHT_API_URL overrides with an external server (also used by e2e
	// tests).
	memManager := hindsight.NewManager(func(ctx context.Context) hindsight.OpLLMConfigs {
		q := db.New(database)
		retain, hasRetain := resolveHindsightLLMConfig(ctx, q, db.PurposeHindsightRetain)
		consolidation, hasConsolidation := resolveHindsightLLMConfig(ctx, q, db.PurposeHindsightConsolidation)
		reflect, hasReflect := resolveHindsightLLMConfig(ctx, q, db.PurposeHindsightReflect)
		return hindsight.OpLLMConfigs{
			Retain:           retain,
			HasRetain:        hasRetain,
			Consolidation:    consolidation,
			HasConsolidation: hasConsolidation,
			Reflect:          reflect,
			HasReflect:       hasReflect,
		}
	})
	memService := hindsight.NewService(db.New(database), memManager.Client)
	eng.SetMemoryService(memService)
	endpoints.SetMemoryService(memService, memManager)

	// Memory rides the backup archive: banks are exported to the hindsight/
	// directory (a backed-up fileDirs entry — see pkg/backup) before each
	// backup and re-imported after a restore.
	backup.PreBackupHook = func(ctx context.Context) {
		dir := filesystem.NewPaths(endpoints.LoadSettings().BasePath).HindsightDir()
		if err := memService.ExportAllToDir(ctx, dir); err != nil {
			log.Printf("Warning: memory export before backup failed: %v", err)
		}
	}
	backup.PostRestoreHook = func() {
		dir := filesystem.NewPaths(endpoints.LoadSettings().BasePath).HindsightDir()
		if err := memService.ImportAllFromDir(context.Background(), dir); err != nil {
			log.Printf("Warning: memory import after restore failed: %v", err)
		}
	}

	// After a schema fallback (the previous schema was migrated by an
	// incompatible hindsight-api build), memories are recovered through the
	// same export/import path: the newest backup export on disk is imported
	// into the fresh schema via Hindsight's own API.
	memManager.RecoverFromExport = func(ctx context.Context) string {
		dir := filesystem.NewPaths(endpoints.LoadSettings().BasePath).HindsightDir()
		return memService.RecoverFromExportDir(ctx, dir)
	}

	// Run setup script and npm installs in the background so the HTTP server starts immediately.
	go func() {
		if err := setup.Run(); err != nil {
			log.Printf("WARNING: startup setup failed — some features may be unavailable: %v", err)
		}
		srv.InstallMCPNpmDeps(context.Background())
		srv.CacheMCPTools(context.Background())

		// Bring the memory backend up (after setup so hindsight-api is
		// installed), then feed every project's docs into it. Hindsight's
		// LLM traffic routes through this server's own gateway, so wait for
		// our HTTP listener to come up first — otherwise hindsight-api's
		// early requests would hit a connection refused.
		waitForOwnServer(60 * time.Second)
		if err := memManager.Start(context.Background()); err != nil {
			log.Printf("WARNING: memory layer unavailable: %v", err)
			return
		}
		srv.SyncAllProjectMemory(context.Background())

		// Refresh the on-disk memory export right after a successful start,
		// so the recovery material a future schema fallback imports from is
		// at most as stale as the last boot (not just the last daily backup).
		exportDir := filesystem.NewPaths(endpoints.LoadSettings().BasePath).HindsightDir()
		if err := memService.ExportAllToDir(context.Background(), exportDir); err != nil {
			log.Printf("Warning: post-start memory export failed: %v", err)
		}
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
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down…")
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

// waitForOwnServer polls this server's own /api/ping until the HTTP listener
// answers, so components that call back into the server (hindsight-api's LLM
// traffic rides our gateway) don't race the listener at startup. Gives up
// after the timeout with a warning rather than blocking forever.
func waitForOwnServer(timeout time.Duration) {
	url := gatewayBaseURL() + "/ping"
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Printf("Warning: own HTTP server not reachable at %s after %v; starting memory backend anyway", url, timeout)
}

// gatewayBaseURL is the loopback address of this server's own LLM gateway,
// used as the base for the /api/proxy/... URLs handed to hindsight-api.
func gatewayBaseURL() string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return "http://127.0.0.1:" + port + "/api"
}

// resolveHindsightLLMConfig resolves the provider+model configured for one
// hindsight purpose (db.PurposeHindsightRetain/Consolidation/Reflect, i.e.
// retain/consolidation/reflect) via the "Default Models" settings — an
// unconfigured purpose means that operation has no override (retain: no LLM
// at all; consolidation/reflect: hindsight-api falls back to the retain LLM
// itself). Rather than the provider's own URL, hindsight is pointed at this
// server's LLM gateway (/api/proxy/group/... or /api/proxy/provider/...), so
// its traffic gets the gateway's proxy logging and token stats, and a model
// group keeps its live failover/health routing instead of being flattened to
// one member at hindsight-api startup.
// firstConfiguredDefaultModelSetting finds the first user with a configured
// (provider or model-group) Default Models setting for purpose. Default
// Model Settings are per-user, but the Hindsight memory backend is one
// shared bare-metal process for the whole instance (its LLM routing is
// baked into env vars at process spawn, not resolved per request) — so
// there is no single company/task to scope this to. Picking the first
// configured user's setting matches the common single-operator deployment
// and is deterministic when several users have configured it.
func firstConfiguredDefaultModelSetting(ctx context.Context, q *db.Queries, purpose string) (db.DefaultModelSetting, bool) {
	users, err := q.ListUsers(ctx)
	if err != nil {
		return db.DefaultModelSetting{}, false
	}
	for _, u := range users {
		setting, err := q.GetDefaultModelSetting(ctx, u.ID, purpose)
		if err != nil {
			continue
		}
		if setting.ProviderID != nil || setting.ModelGroupID != nil {
			return setting, true
		}
	}
	return db.DefaultModelSetting{}, false
}

func resolveHindsightLLMConfig(ctx context.Context, q *db.Queries, purpose string) (hindsight.LLMConfig, bool) {
	setting, ok := firstConfiguredDefaultModelSetting(ctx, q, purpose)
	if !ok {
		return hindsight.LLMConfig{}, false
	}

	if setting.ModelGroupID != nil {
		group, gErr := q.GetModelGroup(ctx, *setting.ModelGroupID)
		if gErr != nil || len(db.ExpandModelGroupMembers(group.Members)) == 0 {
			return hindsight.LLMConfig{}, false
		}
		// The group's slug is a routable pseudo-model: the group router
		// substitutes each attempted member's real model into the body.
		return hindsight.LLMConfig{
			BaseURL: fmt.Sprintf("%s/proxy/group/%d/v1", gatewayBaseURL(), group.ID),
			APIKey:  "internal", // gateway injects the real provider key per attempt
			Model:   group.Slug,
		}, true
	}

	if setting.ProviderID != nil {
		provider, pErr := q.GetLLMProvider(ctx, *setting.ProviderID)
		if pErr != nil {
			return hindsight.LLMConfig{}, false
		}
		model := setting.Model
		if model == "" {
			model = provider.DefaultModel
		}
		return hindsight.LLMConfig{
			BaseURL: fmt.Sprintf("%s/proxy/provider/%d/v1", gatewayBaseURL(), provider.ID),
			APIKey:  "internal", // gateway swaps in the provider's real key
			Model:   model,
		}, true
	}

	return hindsight.LLMConfig{}, false
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
