package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
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
	"agent-orchestrator/pkg/backup"
	"agent-orchestrator/pkg/llmdiscovery"
	"agent-orchestrator/pkg/mailer"
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

	dbConnStr := os.Getenv("DATABASE_URL")

	var database *gorm.DB
	var err error

	if strings.HasPrefix(dbConnStr, "postgres://") {
		log.Println("Connecting to PostgreSQL database")
		database, err = gorm.Open(postgres.Open(dbConnStr), &gorm.Config{})
	} else {
		log.Println("Connecting to SQLite database")
		if dbConnStr == "" {
			if utils.IsE2E() {
				dbConnStr = "headcount1-e2e.db"
			} else {
				dbConnStr = "orchestrator.db"
			}
		}
		database, err = gorm.Open(sqlite.Open(dbConnStr), &gorm.Config{})
	}

	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// SQLite needs single connection to avoid WAL visibility issues
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)

	// Enable FK enforcement for SQLite (disabled by default; must be set per-connection).
	if database.Dialector.Name() == "sqlite" {
		if err := database.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			log.Printf("Warning: failed to enable SQLite foreign keys: %v", err)
		}
	}

	log.Println("Running AutoMigrate...")
	err = database.AutoMigrate(
		&db.User{},
		&db.UserKey{},
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

	// Migrate any legacy auth_token fields from MCPServer → MCPAccount.
	if err := db.New(database).MigrateServerTokensToAccounts(context.Background()); err != nil {
		log.Printf("Warning: MCP account migration failed: %v", err)
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
	hub.SetCompanyOwnerResolver(newCompanyOwnerResolver(database))

	eng := engine.NewNativeEngine(database, hub)
	log.Println("Using native engine")

	srv := server.NewServer(database, eng)
	srv.SetHub(hub)

	// Sync database with filesystem on startup
	if err := srv.Sync(context.Background()); err != nil {
		log.Printf("Warning: Initial filesystem sync failed: %v", err)
	}

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

	// Check if backup is needed on startup
	headcount1Home := db.Headcount1Home()
	if backup.ShouldBackupOnStartup(headcount1Home) {
		log.Println("Latest backup is older than 24h, running backup on startup...")
		go func() {
			_, err := backup.CreateBackup(headcount1Home)
			if err != nil {
				log.Printf("Startup backup failed: %v", err)
			}
		}()
	}
	go backup.StartDailyScheduler(headcount1Home)

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

// newCompanyOwnerResolver returns a TTL-cached company → owning-user lookup
// for tenant-scoped WebSocket event delivery. Ownership changes only on
// company creation, so a short TTL is plenty.
func newCompanyOwnerResolver(database *gorm.DB) func(companyID int32) (int32, bool) {
	type entry struct {
		owner int32
		ok    bool
		at    time.Time
	}
	var mu sync.Mutex
	cache := map[int32]entry{}
	const ttl = 30 * time.Second
	q := db.New(database)
	return func(companyID int32) (int32, bool) {
		mu.Lock()
		e, hit := cache[companyID]
		mu.Unlock()
		if hit && time.Since(e.at) < ttl {
			return e.owner, e.ok
		}
		company, err := q.GetCompany(context.Background(), companyID)
		owner, ok := int32(0), false
		if err == nil && company.UserID != nil {
			owner, ok = *company.UserID, true
		}
		mu.Lock()
		cache[companyID] = entry{owner: owner, ok: ok, at: time.Now()}
		mu.Unlock()
		return owner, ok
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
