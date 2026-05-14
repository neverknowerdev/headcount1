package main

import (
	"path/filepath"
	"fmt"

	"runtime"

	"embed"
	"io/fs"
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/integration"
	"agent-orchestrator/server"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

//go:embed all:frontend/dist
var frontendDist embed.FS

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
			dbConnStr = "orchestrator.db"
		}
		database, err = gorm.Open(sqlite.Open(dbConnStr), &gorm.Config{})
	}

	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
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
		&db.ActivityLog{},
		&db.ProxyRequestLog{},
	)
	if err != nil {
		log.Fatalf("AutoMigrate failed: %v", err)
	}

	hub := eventhub.NewHub()
	eng := engine.NewOpenCodeEngine(database, hub)
	srv := server.NewServer(database, eng)
	srv.SetHub(hub)

	// Sync database with filesystem on startup
	if err := srv.Sync(context.Background()); err != nil {
		log.Printf("Warning: Initial filesystem sync failed: %v", err)
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
		if os.Getenv("E2E_MODE") == "true" {
			log.Println("E2E mode enabled - mock middleware active")
			r.Use(srv.E2EMockMiddleware)
		}
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("pong"))
		})
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

func deployOpenCodeCustomTool() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("Failed to get home dir for custom tool: %w", err)
	}

	paperclipDir := filepath.Join(homeDir, ".paperclip2")
	if err := os.MkdirAll(paperclipDir, 0755); err != nil {
		log.Printf("Failed to create paperclip2 dir: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	serverUrl := "http://127.0.0.1:" + port
	err = os.WriteFile(filepath.Join(paperclipDir, "server_url.txt"), []byte(serverUrl), 0644)
	if err != nil {
		log.Printf("Failed to write server_url.txt: %v", err)
	}

	scriptContentBytes, err := os.ReadFile("templates/opencode_custom_tools/update_task_status.ts")
	if err != nil {
		return fmt.Errorf("Failed to read templates/opencode_custom_tools/update_task_status.ts: %w", err)
	}
	scriptContent := string(scriptContentBytes)

	opencodeToolsDir := filepath.Join(homeDir, ".config", "opencode", "tools")
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
