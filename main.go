package main

import (
	"database/sql"
	"agent-orchestrator/db/migration"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "github.com/lib/pq"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"agent-orchestrator/server"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/engine"
	"agent-orchestrator/integration"
)

//go:embed all:frontend/dist
var frontendDist embed.FS

func main() {
	dbConnStr := os.Getenv("DATABASE_URL")
	if dbConnStr == "" {
		dbConnStr = "postgres://postgres:postgres@localhost:5432/orchestrator?sslmode=disable"
	}

	database, err := sql.Open("postgres", dbConnStr)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()



	runDBMigration(database)

	if err := database.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	hub := eventhub.NewHub()
	eng := engine.NewForgeEngine(database, hub)
	srv := server.NewServer(database, eng)
	srv.SetHub(hub)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
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

func runDBMigration(db *sql.DB) {
	d, err := iofs.New(migration.FS, ".")
	if err != nil {
		log.Fatalf("failed to create migration iofs: %v", err)
	}

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		log.Fatalf("failed to create migration driver: %v", err)
	}

	m, err := migrate.NewWithInstance(
		"iofs", d,
		"postgres", driver,
	)
	if err != nil {
		log.Fatalf("failed to create migrate instance: %v", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatalf("failed to run migrate up: %v", err)
	}

	log.Println("Database migration completed successfully")
}
