package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
	"agent-orchestrator/pkg/hindsight"
	"agent-orchestrator/pkg/llmdiscovery"
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
	)
	if err != nil {
		log.Fatalf("AutoMigrate failed: %v", err)
	}

	recoverStaleRuns(database)

	// Seed predefined MCP servers (paperclip2, github, google-docs) if not present.
	if err := db.New(database).EnsureBuiltinMCPServers(context.Background()); err != nil {
		log.Printf("Warning: failed to seed built-in MCP servers: %v", err)
	}

	// Seed the built-in free-model providers (OpenRouter, OpenCode Zen) if not
	// present. Their model catalog (and DefaultModel) is populated purely
	// from a live fetch in the background — no hardcoded model list — so a
	// slow/unreachable host never delays server startup, and a provider is
	// simply left blank until the first successful fetch completes.
	if err := db.New(database).EnsureBuiltinLLMProviders(context.Background()); err != nil {
		log.Printf("Warning: failed to seed built-in LLM providers: %v", err)
	}
	// Seed the known provider presets (OpenCode Go, MiniMax, ...) users can
	// pick from a dropdown when adding a provider. Unlike the builtin free
	// providers above, these don't become actual LLMProvider rows until a
	// user picks one and supplies an API key.
	if err := db.New(database).EnsureProviderPresets(context.Background()); err != nil {
		log.Printf("Warning: failed to seed provider presets: %v", err)
	}
	// Seed the "Default Models" purposes (commit messages, ask_artifact) with
	// no target configured, so they fall back to the calling session's own
	// LLM until a user points them at a provider/model or a model group.
	if err := db.New(database).EnsureDefaultModelSettings(context.Background()); err != nil {
		log.Printf("Warning: failed to seed default model settings: %v", err)
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

	hub := eventhub.NewHub()

	eng := engine.NewNativeEngine(database, hub)
	log.Println("Using native engine")

	srv := server.NewServer(database, eng)
	srv.SetHub(hub)

	// Sync database with filesystem on startup
	if err := srv.Sync(context.Background()); err != nil {
		log.Printf("Warning: Initial filesystem sync failed: %v", err)
	}

	// Hindsight long-term memory layer. The manager runs a bare-metal
	// hindsight-api process (installed into the app venv by the setup script)
	// configured with the "Default Models" purpose hindsight_memory as its
	// LLM (a fixed provider+model, or a model group's best free member);
	// HINDSIGHT_API_URL overrides with an external server (also used by e2e
	// tests).
	memManager := hindsight.NewManager(func(ctx context.Context) hindsight.OpLLMConfigs {
		q := db.New(database)
		retain, hasRetain := resolveHindsightLLMConfig(ctx, q, db.PurposeHindsightMemory)
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

	// Memory rides the backup archive: banks are exported to data/hindsight
	// before each backup and re-imported after a restore.
	backup.PreBackupHook = func(ctx context.Context) {
		dir := filepath.Join(endpoints.LoadSettings().BasePath, "data", "hindsight")
		if err := memService.ExportAllToDir(ctx, dir); err != nil {
			log.Printf("Warning: memory export before backup failed: %v", err)
		}
	}
	backup.PostRestoreHook = func() {
		dir := filepath.Join(endpoints.LoadSettings().BasePath, "data", "hindsight")
		if err := memService.ImportAllFromDir(context.Background(), dir); err != nil {
			log.Printf("Warning: memory import after restore failed: %v", err)
		}
	}

	// Run setup script and npm installs in the background so the HTTP server starts immediately.
	go func() {
		if err := setup.Run(); err != nil {
			log.Printf("WARNING: startup setup failed — some features may be unavailable: %v", err)
		}
		srv.InstallMCPNpmDeps(context.Background())
		srv.CacheMCPTools(context.Background())

		// Bring the memory backend up (after setup so hindsight-api is
		// installed), then feed every project's docs into it.
		if err := memManager.Start(context.Background()); err != nil {
			log.Printf("WARNING: memory layer unavailable: %v", err)
			return
		}
		srv.SyncAllProjectMemory(context.Background())
	}()
	go srv.StartMCPCacheScheduler(context.Background())

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

// resolveHindsightLLMConfig resolves the provider+model configured for one
// hindsight purpose (db.PurposeHindsightMemory/Consolidation/Reflect, i.e.
// retain/consolidation/reflect) via the "Default Models" settings, mirroring
// engine.NativeEngine.resolvePurposeModel's model group handling but with no
// session provider/model to fall back to — an unconfigured purpose means
// that operation has no override (retain: no LLM at all; consolidation/
// reflect: hindsight-api falls back to the retain LLM itself).
func resolveHindsightLLMConfig(ctx context.Context, q *db.Queries, purpose string) (hindsight.LLMConfig, bool) {
	setting, err := q.GetDefaultModelSetting(ctx, purpose)
	if err != nil {
		return hindsight.LLMConfig{}, false
	}

	if setting.ModelGroupID != nil {
		group, gErr := q.GetModelGroup(ctx, *setting.ModelGroupID)
		if gErr != nil {
			return hindsight.LLMConfig{}, false
		}
		members := db.ExpandModelGroupMembers(group.Members)
		if len(members) == 0 {
			return hindsight.LLMConfig{}, false
		}
		sort.SliceStable(members, func(i, j int) bool {
			if members[i].IsFree != members[j].IsFree {
				return members[i].IsFree
			}
			return members[i].Priority < members[j].Priority
		})
		best := members[0]
		return hindsight.LLMConfig{BaseURL: best.Provider.BaseUrl, APIKey: best.Provider.ApiKey, Model: best.Model}, true
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
		return hindsight.LLMConfig{BaseURL: provider.BaseUrl, APIKey: provider.ApiKey, Model: model}, true
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
