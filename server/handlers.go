package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/setup"
	"agent-orchestrator/server/controllers"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

type Server struct {
	db     *gorm.DB
	q      db.Querier
	hub    *eventhub.Hub
	engine engine.Engine
}

func NewServer(database *gorm.DB, eng engine.Engine) *Server {
	return &Server{
		db:     database,
		q:      db.New(database),
		engine: eng,
	}
}

func (s *Server) SetHub(h *eventhub.Hub) { s.hub = h }

func (s *Server) Sync(ctx context.Context) error {
	api := endpoints.NewAPI(s.db, s.engine, s.hub)
	return api.SyncDBWithFilesystem(ctx)
}

func (s *Server) CacheMCPTools(ctx context.Context) {
	api := endpoints.NewAPI(s.db, s.engine, s.hub)
	api.DiscoverAndCacheAllMCPTools(ctx)
}

// InitPendingCodegraphServers runs codegraph init for any project whose
// codegraph server is not yet in the "ready" state and whose repo is on disk.
func (s *Server) InitPendingCodegraphServers(ctx context.Context) {
	api := endpoints.NewAPI(s.db, s.engine, s.hub)
	api.InitPendingCodegraphServers(ctx)
}

// StartMCPCacheScheduler refreshes the MCP tool cache every 24 hours.
// Run in a goroutine — blocks until ctx is cancelled.
func (s *Server) StartMCPCacheScheduler(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			log.Println("MCP cache: running scheduled refresh...")
			s.CacheMCPTools(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// InstallMCPNpmDeps reads the Deps field from every MCP server in the database
// and installs any npm packages that are not already present globally.
// Runs after the platform setup script so npm is guaranteed to exist.
func (s *Server) InstallMCPNpmDeps(ctx context.Context) {
	pkgs, err := db.New(s.db).ListAllMCPNpmDeps(ctx)
	if err != nil {
		log.Printf("mcp npm deps: failed to query deps: %v", err)
		return
	}
	if len(pkgs) == 0 {
		return
	}
	depsJSON, _ := json.Marshal(pkgs)
	if err := setup.InstallNpmDeps(ctx, string(depsJSON)); err != nil {
		log.Printf("mcp npm deps: %v", err)
	}
}

func (s *Server) Mount(r chi.Router) {

	go s.hub.Run()

	r.Get("/ws", s.serveWs)

	api := endpoints.NewAPI(s.db, s.engine, s.hub)

	r.Route("/companies", func(r chi.Router) {
		r.Get("/", api.ListCompanies)
		r.Post("/", api.CreateCompany)
		r.Put("/{id}", api.UpdateCompany)
		r.Delete("/{id}", api.DeleteCompany)
	})

	r.Get("/settings", api.GetSettings)
	r.Post("/settings", api.UpdateSettings)
	r.Post("/settings/ssh", api.UploadSSHKey)
	r.Post("/settings/sync", api.SyncSettings)
	r.Get("/activities", api.ListActivities)

	r.Route("/skills", func(r chi.Router) {
		r.Get("/", api.ListSkills)
		r.Post("/", api.CreateSkill)
		r.Get("/{id}/files", api.ListSkillFiles)
		r.Get("/{id}/files/content", api.GetSkillFileContent)
		r.Put("/{id}/files/content", api.UpdateSkillFileContent)
	})

	r.Route("/projects", func(r chi.Router) {
		r.Get("/", api.ListProjects)
		r.Post("/", api.CreateProject)
		r.Get("/{id}", api.GetProject)
		r.Put("/{id}", api.UpdateProject)
		r.Get("/{id}/codegraph", api.GetProjectCodegraph)
	})

	r.Route("/tasks", func(r chi.Router) {
		r.Get("/", api.ListTasks)
		r.Post("/", api.CreateTask)
		r.Get("/{id}", api.GetTask)
		r.Put("/{id}", api.UpdateTask)
		r.Put("/{id}/status", api.UpdateTask)
		r.Get("/{id}/runs", api.ListTaskRuns)
		r.Post("/{id}/rerun", api.RerunTask)
		r.Get("/{id}/artifacts", api.ListTaskArtifacts)
		r.Get("/{id}/artifacts/download", api.DownloadTaskArtifacts)
	})

	r.Get("/artifacts/{id}/download", api.DownloadArtifact)

	r.Get("/agent-configs", api.ListAgentConfigs)

	r.Route("/agents", func(r chi.Router) {
		r.Get("/", api.ListAgents)
		r.Post("/", api.CreateAgent)
		r.Get("/{id}", api.GetAgent)
		r.Put("/{id}", api.UpdateAgent)
		r.Get("/{id}/stats", api.GetAgentStats)
		r.Get("/{id}/runs", api.ListAgentRuns)
	})

	r.Route("/comments", func(r chi.Router) {
		r.Get("/", api.ListComments)
		r.Post("/", api.CreateComment)
	})

	r.Route("/attachments", func(r chi.Router) {
		r.Post("/", api.UploadAttachment)
	})

	r.Route("/sprints", func(r chi.Router) {
		r.Get("/", api.ListSprints)
		r.Post("/", api.CreateSprint)
	})

	r.Route("/runs", func(r chi.Router) {
		r.Get("/session/{sessionID}", api.GetRunBySessionID)
		r.Get("/", api.ListCompanyRuns)
		r.Get("/{id}", api.GetRun)
		r.Get("/{id}/children", api.ListChildRuns)
		r.Post("/{id}/stop", api.StopRun)
	})

	r.Route("/providers", func(r chi.Router) {
		r.Get("/", api.ListProviders)
		r.Get("/presets", api.ListProviderPresets)
		r.Post("/", api.CreateProvider)
		r.Post("/from-preset", api.CreateProviderFromPreset)
		r.Put("/{id}", api.UpdateProvider)
		r.Delete("/{id}", api.DeleteProvider)
		r.Post("/test", api.TestProvider)
		r.Post("/{id}/rediscover", api.RediscoverProviderModels)
	})

	r.Get("/setup-status", func(w http.ResponseWriter, _ *http.Request) {
		pending, ok, errMsg, warning := setup.Status()
		if pending {
			respondJSON(w, http.StatusOK, map[string]interface{}{"pending": true})
		} else if ok {
			respondJSON(w, http.StatusOK, map[string]interface{}{"pending": false, "ok": true, "warning": warning, "warnings": setup.Warnings()})
		} else {
			respondJSON(w, http.StatusOK, map[string]interface{}{"pending": false, "ok": false, "error": errMsg, "warning": warning, "failures": setup.Failures(), "warnings": setup.Warnings()})
		}
	})

	r.Route("/backup", func(r chi.Router) {
		r.Post("/", api.CreateBackup)
		r.Get("/status", api.GetBackupStatus)
		r.Get("/list", api.ListBackups)
		r.Post("/restore", api.RestoreBackup)
	})

	r.Route("/mcp-servers", func(r chi.Router) {
		r.Get("/", api.ListMCPServers)
		r.Post("/", api.CreateMCPServer)
		r.Get("/{id}", api.GetMCPServer)
		r.Put("/{id}", api.UpdateMCPServer)
		r.Delete("/{id}", api.DeleteMCPServer)
		r.Post("/{id}/discover", api.DiscoverMCPServerTools)
		r.Post("/{id}/accounts", api.CreateMCPAccount)
		r.Post("/{id}/google-oauth", api.StartGoogleOAuth)
		r.Get("/{id}/google-oauth", api.PollGoogleOAuth)
	})

	r.Route("/mcp-accounts/{accountID}", func(r chi.Router) {
		r.Put("/", api.UpdateMCPAccount)
		r.Delete("/", api.DeleteMCPAccount)
		r.Post("/discover", api.DiscoverMCPAccountTools)
	})

	r.Route("/agents/{id}/mcp-servers", func(r chi.Router) {
		r.Get("/", api.GetAgentMCPServers)
		r.Put("/", api.SetAgentMCPServers)
	})

	r.Route("/agents/{id}/mcp-accounts", func(r chi.Router) {
		r.Get("/", api.GetAgentMCPAccounts)
		r.Put("/", api.SetAgentMCPAccounts)
	})

	r.Route("/agents/{id}/mcp-tool-filters", func(r chi.Router) {
		r.Get("/", api.GetAgentMCPToolFilters)
		r.Put("/", api.SetAgentMCPToolFilters)
	})
}

func respondJSON(w http.ResponseWriter, status int, payload interface{}) {
	response, err := json.Marshal(payload)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(response)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (s *Server) serveWs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.hub.Serve(conn)
}
