package endpoints

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/filesystem"
	"gorm.io/gorm"
)

type API struct {
	db     *gorm.DB
	q      *db.Queries
	engine engine.Engine
	hub    *eventhub.Hub
}

func NewAPI(database *gorm.DB, eng engine.Engine, h *eventhub.Hub) *API {
	return &API{
		db:     database,
		q:      db.New(database),
		engine: eng,
		hub:    h,
	}
}

// currentUserID returns the authenticated user's ID from the request context
// (non-zero for any handler mounted behind RequireAuth).
func (api *API) currentUserID(r *http.Request) int32 {
	return authctx.UserID(r.Context())
}

// errNotOwned is returned by the authorize* helpers on any ownership
// mismatch. Handlers translate it (and load failures) to 404 — never 403 —
// so other tenants' IDs are indistinguishable from nonexistent ones.
var errNotOwned = errors.New("not found")

// authorizeCompany returns the company iff the authenticated user owns it.
func (api *API) authorizeCompany(r *http.Request, companyID int32) (db.Company, error) {
	company, err := api.q.GetCompany(r.Context(), companyID)
	if err != nil {
		return db.Company{}, err
	}
	uid := api.currentUserID(r)
	if company.UserID == nil || *company.UserID != uid {
		return db.Company{}, errNotOwned
	}
	return company, nil
}

// authorizeTask returns the task iff its company is owned by the ctx user.
func (api *API) authorizeTask(r *http.Request, taskID int32) (db.Task, error) {
	task, err := api.q.GetTask(r.Context(), taskID)
	if err != nil {
		return db.Task{}, err
	}
	if _, err := api.authorizeCompany(r, task.CompanyID); err != nil {
		return db.Task{}, err
	}
	return task, nil
}

// authorizeRun returns the run iff its task's company is owned by the ctx user.
func (api *API) authorizeRun(r *http.Request, runID int32) (db.Run, error) {
	run, task, err := api.q.GetRunWithTask(r.Context(), runID)
	if err != nil {
		return db.Run{}, err
	}
	if _, err := api.authorizeCompany(r, task.CompanyID); err != nil {
		return db.Run{}, err
	}
	return run, nil
}

// authorizeAgent returns the agent iff its company is owned by the ctx user.
func (api *API) authorizeAgent(r *http.Request, agentID int32) (db.Agent, error) {
	agent, err := api.q.GetAgent(r.Context(), agentID)
	if err != nil {
		return db.Agent{}, err
	}
	if _, err := api.authorizeCompany(r, agent.CompanyID); err != nil {
		return db.Agent{}, err
	}
	return agent, nil
}

// authorizeProject returns the project iff its company is owned by the ctx user.
func (api *API) authorizeProject(r *http.Request, projectID int32) (db.Project, error) {
	project, err := api.q.GetProject(r.Context(), projectID)
	if err != nil {
		return db.Project{}, err
	}
	if _, err := api.authorizeCompany(r, project.CompanyID); err != nil {
		return db.Project{}, err
	}
	return project, nil
}

// authorizeSkill returns the skill iff its company is owned by the ctx user.
func (api *API) authorizeSkill(r *http.Request, skillID int32) (db.Skill, error) {
	var skill db.Skill
	if err := api.db.WithContext(r.Context()).First(&skill, skillID).Error; err != nil {
		return db.Skill{}, err
	}
	if _, err := api.authorizeCompany(r, skill.CompanyID); err != nil {
		return db.Skill{}, err
	}
	return skill, nil
}

// authorizeMCPServer returns the server iff the ctx user may act on it:
// builtin catalog rows (shared; per-user state lives in MCPAccount), the
// user's own custom servers, and codegraph servers of the user's projects.
func (api *API) authorizeMCPServer(r *http.Request, serverID int32) (db.MCPServer, error) {
	s, err := api.q.GetMCPServer(r.Context(), serverID)
	if err != nil {
		return db.MCPServer{}, err
	}
	switch {
	case s.Builtin:
		return s, nil
	case s.ProjectID != nil:
		if _, err := api.authorizeProject(r, *s.ProjectID); err != nil {
			return db.MCPServer{}, err
		}
		return s, nil
	case ownedByUser(r, s.OwnerUserID):
		return s, nil
	}
	return db.MCPServer{}, errNotOwned
}

// filterAccountsForUser keeps only the ctx user's own MCP credentials.
func filterAccountsForUser(r *http.Request, accounts []db.MCPAccount) []db.MCPAccount {
	own := accounts[:0]
	for _, a := range accounts {
		if ownedByUser(r, a.UserID) {
			own = append(own, a)
		}
	}
	return own
}

// ownedByUser reports whether a per-user row (provider, model group, MCP
// account, ...) belongs to the ctx user.
func ownedByUser(r *http.Request, rowUserID *int32) bool {
	return rowUserID != nil && *rowUserID == authctx.UserID(r.Context())
}

func (api *API) respondJSON(w http.ResponseWriter, status int, data interface{}) {
	response, err := json.Marshal(data)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(response)
}

func (api *API) respondError(w http.ResponseWriter, status int, message string) {
	api.respondJSON(w, status, map[string]string{"error": message})
}

func (api *API) logActivity(companyID int32, action string, entityID int32, entityType string, details string) {
	activityLog := db.ActivityLog{
		CompanyID:  companyID,
		Action:     action,
		EntityID:   entityID,
		EntityType: entityType,
		Details:    details,
	}
	api.db.Create(&activityLog)

	// Write activity log to filesystem
	settings := LoadSettings()
	storage := filesystem.NewStorage(settings.BasePath)
	if err := storage.WriteActivityLog(activityLog); err != nil {
		log.Printf("Warning: failed to write activity log to filesystem: %v", err)
	}
}
