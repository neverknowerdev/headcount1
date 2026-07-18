package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/setup"
	endpoints "agent-orchestrator/server/controllers"

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

// MountPublic registers the routes that must work without a login: the auth
// endpoints themselves and the setup-status probe the frontend polls before
// anyone is signed in. Everything else lives in Mount, behind RequireAuth.
func (s *Server) MountPublic(r chi.Router) {
	api := endpoints.NewAPI(s.db, s.engine, s.hub)

	// Passwordless passkey ceremonies (challenge round-trip is the guard).
	// Registered flat (not via r.Route) so the authenticated /auth routes in
	// Mount can share the same "/auth" prefix without a chi subrouter clash.
	r.Post("/auth/register/begin", api.RegisterBegin)
	r.Post("/auth/register/finish", api.RegisterFinish)
	r.Post("/auth/login/begin", api.LoginBegin)
	r.Post("/auth/login/finish", api.LoginFinish)
	r.Post("/auth/logout", api.Logout)
	r.Get("/auth/me", api.Me)
	// Rotating access/refresh: the browser exchanges its refresh token for a
	// new pair here. Public (the refresh cookie is the credential); the cookie
	// is path-scoped to exactly this route.
	r.Post("/auth/refresh", api.Refresh)
	// Email-based passkey recovery (wipes secrets, preserves the account).
	r.Post("/auth/recover/request", api.RecoverRequest)
	r.Post("/auth/recover/confirm", api.RecoverConfirm)

	// Public: lets the register page show which team an invite joins (the
	// token itself is the credential).
	r.Get("/invite-info", api.InviteInfo)

	r.Get("/setup-status", func(w http.ResponseWriter, _ *http.Request) {
		pending, ok, errMsg, warning := setup.Status()
		if pending {
			respondJSON(w, http.StatusOK, map[string]interface{}{"pending": true, "step": setup.CurrentStep()})
		} else if ok {
			respondJSON(w, http.StatusOK, map[string]interface{}{"pending": false, "ok": true, "warning": warning, "warnings": setup.Warnings()})
		} else {
			respondJSON(w, http.StatusOK, map[string]interface{}{"pending": false, "ok": false, "error": errMsg, "warning": warning, "failures": setup.Failures(), "warnings": setup.Warnings()})
		}
	})
}

// AuthMiddleware returns the session middleware used to gate Mount's routes.
func (s *Server) AuthMiddleware() func(http.Handler) http.Handler {
	return endpoints.NewAPI(s.db, s.engine, s.hub).RequireAuth
}

// CSRFMiddleware returns the double-submit CSRF guard for the authenticated
// human-facing API. Mounted alongside AuthMiddleware in the authed group.
func (s *Server) CSRFMiddleware() func(http.Handler) http.Handler {
	return endpoints.NewAPI(s.db, s.engine, s.hub).CSRF
}

func (s *Server) Mount(r chi.Router) {

	go s.hub.Run()

	r.Get("/ws", s.serveWs)

	api := endpoints.NewAPI(s.db, s.engine, s.hub)

	// Authenticated passkey operations: crash re-tap unlock (session present,
	// keyring cold) and managing enrolled credentials (must be unlocked).
	// Flat routes (not r.Route) to avoid a subrouter clash with MountPublic's
	// "/auth" routes.
	r.Post("/auth/unlock/begin", api.UnlockBegin)
	r.Post("/auth/unlock/finish", api.UnlockFinish)
	r.Get("/auth/credentials", api.ListCredentials)
	r.Post("/auth/credentials/begin", api.AddCredentialBegin)
	r.Post("/auth/credentials/finish", api.AddCredentialFinish)

	r.Route("/companies", func(r chi.Router) {
		r.Get("/", api.ListCompanies)
		r.Post("/", api.CreateCompany)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(api.LoadCompany)
			r.Put("/", api.UpdateCompany)
			r.Delete("/", api.DeleteCompany)
		})
	})

	r.Route("/team", func(r chi.Router) {
		r.Get("/", api.GetTeam)
		r.Put("/", api.UpdateTeam)
		r.Post("/invites", api.CreateTeamInvite)
		r.Delete("/invites/{id}", api.DeleteTeamInvite)
	})

	r.Get("/settings", api.GetSettings)
	// UpdateSettings mutates the instance-global config (base path, workspace
	// layout) — operator-only. UploadSSHKey is per-user (see settings.go).
	r.Group(func(r chi.Router) {
		r.Use(api.RequireGlobalAdminAPI)
		r.Post("/settings", api.UpdateSettings)
	})
	r.Post("/settings/ssh", api.UploadSSHKey)
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
		r.Post("/test", api.TestProvider)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(api.LoadProvider)
			r.Put("/", api.UpdateProvider)
			r.Delete("/", api.DeleteProvider)
			r.Post("/rediscover", api.RediscoverProviderModels)
		})
	})

	r.Route("/model-groups", func(r chi.Router) {
		r.Get("/", api.ListModelGroups)
		r.Post("/", api.CreateModelGroup)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(api.LoadModelGroup)
			r.Put("/", api.UpdateModelGroup)
			r.Delete("/", api.DeleteModelGroup)
			r.Get("/stats", api.GetModelGroupStats)
		})
	})

	r.Route("/default-model-settings", func(r chi.Router) {
		r.Get("/", api.ListDefaultModelSettings)
		r.Put("/{purpose}", api.UpdateDefaultModelSetting)
	})

	// Backup/restore act on the WHOLE multi-tenant instance (a restore wipes and
	// replaces every tenant), so the HTTP surface is operator-gated. Off by
	// default; scheduled server-side backups still run regardless.
	r.Route("/backup", func(r chi.Router) {
		r.Use(api.RequireGlobalAdminAPI)
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
	// Same-origin only: the cookie authenticates the upgrade, so a cross-site
	// page must not be able to open an authenticated socket. Non-browser
	// clients (tests, tools) send no Origin header and are allowed.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	},
}

func (s *Server) serveWs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	// Mounted behind RequireAuth, so the user is always in context; events
	// are delivered per-tenant based on this ID.
	s.hub.Serve(conn, authctx.UserID(r.Context()))
}
