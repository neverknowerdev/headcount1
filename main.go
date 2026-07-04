package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/integration"
	"agent-orchestrator/pkg/backup"
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
				dbConnStr = "paperclip-e2e.db"
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
		&db.Company{},
		&db.Project{},
		&db.Sprint{},
		&db.LLMProvider{},
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
		&db.MemoryActivity{},
	)
	if err != nil {
		log.Fatalf("AutoMigrate failed: %v", err)
	}

	recoverStaleRuns(database)

	// Seed predefined MCP servers (paperclip2, github, google-docs) if not present.
	if err := db.New(database).EnsureBuiltinMCPServers(context.Background()); err != nil {
		log.Printf("Warning: failed to seed built-in MCP servers: %v", err)
	}

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

	hub := eventhub.NewHub()

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
		// Memory palaces can only be provisioned once setup has decided
		// whether mempalace is available.
		srv.EnsureCompanyPalaces(context.Background())
	}()
	go srv.StartMCPCacheScheduler(context.Background())
	go srv.StartMemoryActivityPurge(context.Background())

	// Resume codegraph init for any project whose knowledge graph isn't ready yet.
	go srv.InitPendingCodegraphServers(context.Background())

	// Check if backup is needed on startup
	paperclipHome := db.PaperclipHome()
	if backup.ShouldBackupOnStartup(paperclipHome) {
		log.Println("Latest backup is older than 24h, running backup on startup...")
		go func() {
			_, err := backup.CreateBackup(paperclipHome)
			if err != nil {
				log.Printf("Startup backup failed: %v", err)
			}
		}()
	}
	go backup.StartDailyScheduler(paperclipHome)

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

		srv.Mount(r)

		gw := integration.NewLLMGatewayWithHub(database, hub)
		gw.Mount(r)
	})

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
