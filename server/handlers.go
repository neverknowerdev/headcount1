package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/setup"
	"agent-orchestrator/pkg/updater"
	endpoints "agent-orchestrator/server/controllers"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

type Server struct {
	db      *gorm.DB
	q       db.Querier
	hub     *eventhub.Hub
	engine  engine.Engine
	updater *updater.Updater
}

func NewServer(database *gorm.DB, eng engine.Engine) *Server {
	return &Server{
		db:     database,
		q:      db.New(database),
		engine: eng,
	}
}

func (s *Server) SetUpdater(upd *updater.Updater) { s.updater = upd }

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

// InstallMCPDependencies installs and verifies dependencies one MCP server at a
// time. Errors are persisted on that server so the MCP page can identify the
// affected integration and offer a retry instead of surfacing a platform-wide
// setup failure.
func (s *Server) InstallMCPDependencies(ctx context.Context) {
	q := db.New(s.db)
	servers, err := q.ListMCPServers(ctx, 0, 0)
	if err != nil {
		log.Printf("mcp deps: failed to list servers: %v", err)
		return
	}
	for _, mcpServer := range servers {
		installCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := setup.InstallMCPDependencies(installCtx, mcpServer)
		cancel()
		if err != nil {
			message := "Dependency setup failed: " + err.Error()
			_ = q.UpdateMCPServerLastError(ctx, mcpServer.ID, message)
			log.Printf("mcp deps: %s: %v", mcpServer.Name, err)
			continue
		}
		if strings.HasPrefix(mcpServer.LastError, "Dependency setup failed:") {
			_ = q.UpdateMCPServerLastError(ctx, mcpServer.ID, "")
		}
	}
}

// MountPublic registers the routes that must work without a login: the auth
// endpoints themselves and the setup-status probe the frontend polls before
// anyone is signed in. Everything else lives in Mount, behind RequireAuth.
func (s *Server) MountPublic(r chi.Router) {
	api := endpoints.NewAPI(s.db, s.engine, s.hub).SetUpdater(s.updater)

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

	// Public deploy webhook: CI (not a user session) posts build/deploy events
	// here. It authenticates with the shared HEADCOUNT1_DEPLOY_API_KEY, and is
	// a no-op unless that key is configured — see DeployWebhook.
	r.Post("/deploy/webhook", api.DeployWebhook)
	// GitHub App deliveries cannot carry a Headcount1 browser session. Their
	// HMAC signature is verified by GitHubWebhook before any processing.
	r.Post("/github/webhook", api.GitHubWebhook)

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

	api := endpoints.NewAPI(s.db, s.engine, s.hub).SetUpdater(s.updater)

	// Authenticated passkey operations: crash re-tap unlock (session present,
	// keyring cold) and managing enrolled credentials (must be unlocked).
	// Flat routes (not r.Route) to avoid a subrouter clash with MountPublic's
	// "/auth" routes.
	r.Post("/auth/unlock/begin", api.UnlockBegin)
	r.Post("/auth/unlock/finish", api.UnlockFinish)
	// Proactive re-auth before the absolute session cap (resets the ceiling +
	// re-warms the keyring) so long-lived logins never lapse mid-run.
	r.Post("/auth/reauth/begin", api.ReauthBegin)
	r.Post("/auth/reauth/finish", api.ReauthFinish)
	r.Get("/auth/credentials", api.ListCredentials)
	r.Post("/auth/credentials/begin", api.AddCredentialBegin)
	r.Post("/auth/credentials/finish", api.AddCredentialFinish)

	r.Route("/companies", func(r chi.Router) {
		r.Get("/", api.ListCompanies)
		// Creating and deleting a company are owner-only structural actions.
		r.With(api.RequireTeamOwner).Post("/", api.CreateCompany)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(api.LoadCompany)
			r.Put("/", api.UpdateCompany)
			r.With(api.RequireTeamOwner).Delete("/", api.DeleteCompany)
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
	r.Route("/github", func(r chi.Router) {
		r.Get("/status", api.GitHubStatus)
		r.Get("/callback", api.GitHubCallback)
		r.Get("/repositories", api.ListGitHubRepositories)
	})
	r.Get("/activities", api.ListActivities)

	r.Route("/skills", func(r chi.Router) {
		r.Get("/", api.ListSkills)
		r.Post("/", api.CreateSkill)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(api.LoadSkill)
			r.Get("/files", api.ListSkillFiles)
			r.Get("/files/content", api.GetSkillFileContent)
			r.Put("/files/content", api.UpdateSkillFileContent)
		})
	})

	r.Route("/projects", func(r chi.Router) {
		r.Get("/", api.ListProjects)
		// Creating a project is an owner-only structural action.
		r.With(api.RequireTeamOwner).Post("/", api.CreateProject)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(api.LoadProject)
			r.Get("/", api.GetProject)
			r.Put("/", api.UpdateProject)
			r.Get("/codegraph", api.GetProjectCodegraph)
		})
	})

	r.Route("/tasks", func(r chi.Router) {
		r.Get("/", api.ListTasks)
		r.Post("/", api.CreateTask)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(api.LoadTask)
			r.Get("/", api.GetTask)
			r.Put("/", api.UpdateTask)
			r.Put("/status", api.UpdateTask)
			r.Get("/runs", api.ListTaskRuns)
			r.Post("/rerun", api.RerunTask)
			r.Get("/artifacts", api.ListTaskArtifacts)
			r.Get("/artifacts/download", api.DownloadTaskArtifacts)
		})
	})

	r.Get("/artifacts/{id}/download", api.DownloadArtifact)

	r.Get("/agent-configs", api.ListAgentConfigs)

	r.Route("/agents", func(r chi.Router) {
		r.Get("/", api.ListAgents)
		r.Post("/", api.CreateAgent)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(api.LoadAgent)
			r.Get("/", api.GetAgent)
			r.Put("/", api.UpdateAgent)
			r.Get("/stats", api.GetAgentStats)
			r.Get("/runs", api.ListAgentRuns)
		})
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
		r.Route("/{id}", func(r chi.Router) {
			r.Use(api.LoadRun)
			r.Get("/", api.GetRun)
			r.Get("/children", api.ListChildRuns)
			r.Post("/stop", api.StopRun)
		})
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

	// Per-user, tenant-scoped export/import: exports the caller's team's
	// subtree and re-imports it into any database (ID-remapped, re-owned to the
	// importer), with zero impact on other tenants. Team-owner-only: the
	// archive covers every company visible to the whole team (and import
	// re-owners a subtree onto it), which is a structural, team-wide action —
	// the same bar as creating/deleting a company.
	r.Route("/data", func(r chi.Router) {
		r.Use(api.RequireTeamOwner)
		r.Get("/export", api.ExportMyData)
		r.Post("/import", api.ImportMyData)
	})

	// The legacy /backup path acts on the WHOLE multi-tenant instance (a restore
	// wipes and replaces every tenant). It is now an operator-only disaster-
	// recovery tool, kept behind the global-admin gate (off by default) and
	// separate from the user-facing /data export/import above. Scheduled
	// server-side backups still run regardless of this HTTP surface.
	r.Route("/backup", func(r chi.Router) {
		r.Use(api.RequireGlobalAdminAPI)
		r.Post("/", api.CreateBackup)
		r.Get("/status", api.GetBackupStatus)
		r.Get("/list", api.ListBackups)
		r.Post("/restore", api.RestoreBackup)
	})

	// Deploy state, read-only for any signed-in user (the running version +
	// this server's environment/source). Deploys themselves are triggered by
	// CI via the public /deploy/webhook, not from here.
	r.Get("/version", api.GetVersion)
	r.Get("/deploy/status", api.GetDeployStatus)

	r.Route("/mcp-servers", func(r chi.Router) {
		r.Get("/", api.ListMCPServers)
		r.Post("/", api.CreateMCPServer)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(api.LoadMCPServer)
			r.Get("/", api.GetMCPServer)
			r.Put("/", api.UpdateMCPServer)
			// Deleting a (shared) MCP server is an owner-only action.
			r.With(api.RequireTeamOwner).Delete("/", api.DeleteMCPServer)
			r.With(api.RequireTeamOwner).Post("/install-dependencies", api.InstallMCPServerDependencies)
			r.Post("/discover", api.DiscoverMCPServerTools)
			r.Post("/accounts", api.CreateMCPAccount)
			r.Post("/github-oauth", api.StartMCPGitHubOAuth)
			r.Post("/google-oauth", api.StartGoogleOAuth)
			r.Get("/google-oauth", api.PollGoogleOAuth)
		})
	})

	r.Route("/mcp-accounts/{accountID}", func(r chi.Router) {
		r.Use(api.LoadMCPAccount)
		r.Put("/", api.UpdateMCPAccount)
		r.Delete("/", api.DeleteMCPAccount)
		r.Post("/discover", api.DiscoverMCPAccountTools)
	})

	r.Route("/agents/{id}/mcp-servers", func(r chi.Router) {
		r.Use(api.LoadAgent)
		r.Get("/", api.GetAgentMCPServers)
		r.Put("/", api.SetAgentMCPServers)
	})

	r.Route("/agents/{id}/mcp-accounts", func(r chi.Router) {
		r.Use(api.LoadAgent)
		r.Get("/", api.GetAgentMCPAccounts)
		r.Put("/", api.SetAgentMCPAccounts)
	})

	r.Route("/agents/{id}/mcp-tool-filters", func(r chi.Router) {
		r.Use(api.LoadAgent)
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
