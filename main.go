package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	"agent-orchestrator/pkg/filesystem"
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
		&db.UserKey{},
		&db.Team{},
		&db.TeamMember{},
		&db.TeamInvite{},
		&db.Session{},
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

	// Per-user encryption keys live in the user_keys table; hand the secrets
	// package its storage before anything can seal a user-owned secret.
	secrets.SetUserKeyStorage(db.NewUserKeyStorage(database))

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

	// Secrets (provider API keys, MCP tokens) are encrypted at rest; seal any
	// rows written before encryption was introduced. On failure the server
	// still starts — reads of already-sealed secrets keep working or fail
	// loudly at the point of use, never silently downgrade to plaintext.
	log.Printf("Secrets encrypted at rest; master key source: %s", db.SecretsBackend())
	if err := db.New(database).EncryptPlaintextSecrets(context.Background()); err != nil {
		log.Printf("Warning: failed to encrypt pre-existing plaintext secrets: %v", err)
	}

	// Transactional mail (password resets): SMTP_* env vars, or a logging
	// no-op mailer that prints the reset link to the server log.
	endpoints.SetMailer(mailer.FromEnv())

	hub := eventhub.NewHub()
	hub.SetCompanyRecipientsResolver(newCompanyRecipientsResolver(database))

	eng := engine.NewNativeEngine(database, hub)
	log.Println("Using native engine")

	srv := server.NewServer(database, eng)
	srv.SetHub(hub)

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
			srv.Mount(r)
		})
	})

	// Expired sessions accumulate silently; sweep them hourly.
	go func() {
		q := db.New(database)
		for {
			time.Sleep(time.Hour)
			if err := q.DeleteExpiredSessions(context.Background()); err != nil {
				log.Printf("session GC failed: %v", err)
			}
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

	log.Printf("Starting server on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
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
