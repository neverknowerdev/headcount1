package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/integration"
	"agent-orchestrator/pkg/backup"
	"agent-orchestrator/pkg/utils"
	"agent-orchestrator/server"
	endpoints "agent-orchestrator/server/controllers"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

//go:embed all:frontend/dist
var frontendDist embed.FS

//go:embed templates/opencode_custom_tools/update_task_status.ts
var updateTaskStatusScript []byte

func main() {
	// Deploy Custom Tool Script for OpenCode
	if err := deployOpenCodeCustomTool(); err != nil {
		log.Fatalf("Failed to deploy OpenCode custom tool: %v", err)
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
		&db.ActivityLog{},
		&db.ProxyRequestLog{},
	)
	if err != nil {
		log.Fatalf("AutoMigrate failed: %v", err)
	}

	recoverStaleRuns(database)

	// Initialize OpenCode agent configs for existing agents
	if err := initOpenCodeAgentConfigs(database); err != nil {
		log.Printf("Failed to initialize OpenCode agent configs: %v", err)
	}

	hub := eventhub.NewHub()
	eng := engine.NewOpenCodeEngine(database, hub)
	srv := server.NewServer(database, eng)
	srv.SetHub(hub)

	// Sync database with filesystem on startup
	if err := srv.Sync(context.Background()); err != nil {
		log.Printf("Warning: Initial filesystem sync failed: %v", err)
	}

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

		gw := integration.NewLLMGateway(database)
		gw.Mount(r)
	})

	distFS, err := fs.Sub(frontendDist, "frontend/dist")
	if err != nil {
		log.Fatalf("Failed to create sub filesystem: %v", err)
	}

	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
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

func initOpenCodeAgentConfigs(database *gorm.DB) error {
	q := db.New(database)

	// Fetch all companies to iterate over their agents
	companies, err := q.ListCompanies(context.Background())
	if err != nil {
		return err
	}

	for _, comp := range companies {
		agents, err := q.ListAgentsByCompany(context.Background(), comp.ID)
		if err != nil {
			continue
		}

		for _, agent := range agents {
			// Get provider type if exists
			providerType := ""
			if agent.ProviderID != nil {
				if p, err := q.GetLLMProvider(context.Background(), *agent.ProviderID); err == nil {
					providerType = p.ProviderType
					if providerType == "" {
						providerType = p.Name
					}
				}
			}

			// Check if file exists, if not create it
			agentPath := filepath.Join(db.OpencodeConfigDir(), "agents", fmt.Sprintf("%s.md", agent.Name))
			if _, err := os.Stat(agentPath); os.IsNotExist(err) {
				log.Printf("Agent config file missing for %s, creating...", agent.Name)
				_ = endpoints.SaveOpenCodeAgentConfig(agent, providerType)
			}
		}
	}
	return nil
}

func deployOpenCodeCustomTool() error {
	paperclipDir := db.PaperclipHome()
	if err := os.MkdirAll(paperclipDir, 0755); err != nil {
		log.Printf("Failed to create paperclip2 dir: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	
	// Detect if running in Docker and use appropriate hostname
	serverHost := "127.0.0.1"
	if _, err := os.Stat("/.dockerenv"); err == nil {
		serverHost = "host.docker.internal"
	} else if os.Getenv("PAPERCLIP_SERVER_HOST") != "" {
		serverHost = os.Getenv("PAPERCLIP_SERVER_HOST")
	}
	
	serverUrl := "http://" + serverHost + ":" + port
	err := os.WriteFile(filepath.Join(paperclipDir, "server_url.txt"), []byte(serverUrl), 0644)
	if err != nil {
		log.Printf("Failed to write server_url.txt: %v", err)
	} else {
		log.Printf("Wrote server URL to %s: %s", filepath.Join(paperclipDir, "server_url.txt"), serverUrl)
	}

	scriptContent := string(updateTaskStatusScript)

	opencodeToolsDir := filepath.Join(db.OpencodeConfigDir(), "tools")
	if runtime.GOOS == "windows" {
		opencodeToolsDir = filepath.Join(os.Getenv("APPDATA"), "opencode", "tools")
	}

	if err := os.MkdirAll(opencodeToolsDir, 0755); err != nil {
		return fmt.Errorf("Failed to create opencode tools dir: %w", err)
	}

	destPath := filepath.Join(opencodeToolsDir, "update_task_status.ts")
	if err := os.WriteFile(destPath, []byte(scriptContent), 0644); err != nil {
		return fmt.Errorf("Failed to write opencode custom tool: %w", err)
	} else {
		log.Printf("Successfully deployed opencode custom tool to %s", destPath)
		return nil
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
