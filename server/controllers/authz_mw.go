package endpoints

import (
	"context"
	"net/http"
	"strconv"

	"agent-orchestrator/db"

	"github.com/go-chi/chi/v5"
)

// This file turns the per-request ownership checks (the authorize* helpers in
// api.go) into route middleware. A loader parses the {id} URL param, runs the
// matching authorize* helper, and — on success — stashes the loaded resource in
// the request context so the handler reads it with an *FromCtx getter instead of
// re-loading and re-checking. Any load failure or ownership mismatch is a 404
// (never 403), preserving the "other tenants' ids look nonexistent" contract.
//
// Handlers keep the special-cases the middleware can't know about (e.g. "can't
// delete a builtin" → 403), which run after the resource is in context.

type ctxKey string

const (
	companyKey    ctxKey = "authz.company"
	taskKey       ctxKey = "authz.task"
	runKey        ctxKey = "authz.run"
	agentKey      ctxKey = "authz.agent"
	projectKey    ctxKey = "authz.project"
	skillKey      ctxKey = "authz.skill"
	mcpServerKey  ctxKey = "authz.mcpServer"
	providerKey   ctxKey = "authz.provider"
	modelGroupKey ctxKey = "authz.modelGroup"
	mcpAccountKey ctxKey = "authz.mcpAccount"
)

// urlID parses an integer chi URL param, writing a 400 on failure.
func (api *API) urlID(w http.ResponseWriter, r *http.Request, param string) (int32, bool) {
	id, err := strconv.Atoi(chi.URLParam(r, param))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return int32(id), true
}

// ── per-user authorize helpers (mirroring the company-scoped ones in api.go) ──

func (api *API) authorizeProvider(r *http.Request, id int32) (db.LLMProvider, error) {
	p, err := api.q.GetLLMProvider(r.Context(), id)
	if err != nil {
		return db.LLMProvider{}, err
	}
	if !ownedByUser(r, p.UserID) {
		return db.LLMProvider{}, errNotOwned
	}
	return p, nil
}

func (api *API) authorizeModelGroup(r *http.Request, id int32) (db.ModelGroup, error) {
	g, err := api.q.GetModelGroup(r.Context(), id)
	if err != nil {
		return db.ModelGroup{}, err
	}
	if !ownedByUser(r, g.UserID) {
		return db.ModelGroup{}, errNotOwned
	}
	return g, nil
}

func (api *API) authorizeMCPAccount(r *http.Request, id int32) (db.MCPAccount, error) {
	a, err := api.q.GetMCPAccount(r.Context(), id)
	if err != nil {
		return db.MCPAccount{}, err
	}
	if !ownedByUser(r, a.UserID) {
		return db.MCPAccount{}, errNotOwned
	}
	return a, nil
}

// ── loader middleware ─────────────────────────────────────────────────────────

func (api *API) LoadCompany(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := api.urlID(w, r, "id")
		if !ok {
			return
		}
		c, err := api.authorizeCompany(r, id)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "company not found")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), companyKey, c)))
	})
}

func (api *API) LoadTask(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := api.urlID(w, r, "id")
		if !ok {
			return
		}
		t, err := api.authorizeTask(r, id)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "task not found")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), taskKey, t)))
	})
}

func (api *API) LoadRun(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := api.urlID(w, r, "id")
		if !ok {
			return
		}
		run, err := api.authorizeRun(r, id)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "run not found")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), runKey, run)))
	})
}

func (api *API) LoadAgent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := api.urlID(w, r, "id")
		if !ok {
			return
		}
		a, err := api.authorizeAgent(r, id)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "agent not found")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), agentKey, a)))
	})
}

func (api *API) LoadProject(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := api.urlID(w, r, "id")
		if !ok {
			return
		}
		p, err := api.authorizeProject(r, id)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "project not found")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), projectKey, p)))
	})
}

func (api *API) LoadSkill(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := api.urlID(w, r, "id")
		if !ok {
			return
		}
		s, err := api.authorizeSkill(r, id)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "skill not found")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), skillKey, s)))
	})
}

func (api *API) LoadMCPServer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := api.urlID(w, r, "id")
		if !ok {
			return
		}
		s, err := api.authorizeMCPServer(r, id)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "not found")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mcpServerKey, s)))
	})
}

func (api *API) LoadProvider(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := api.urlID(w, r, "id")
		if !ok {
			return
		}
		p, err := api.authorizeProvider(r, id)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "provider not found")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), providerKey, p)))
	})
}

func (api *API) LoadModelGroup(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := api.urlID(w, r, "id")
		if !ok {
			return
		}
		g, err := api.authorizeModelGroup(r, id)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "model group not found")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), modelGroupKey, g)))
	})
}

func (api *API) LoadMCPAccount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(chi.URLParam(r, "accountID"))
		if err != nil {
			api.respondError(w, http.StatusBadRequest, "invalid id")
			return
		}
		a, aerr := api.authorizeMCPAccount(r, int32(id))
		if aerr != nil {
			api.respondError(w, http.StatusNotFound, "not found")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mcpAccountKey, a)))
	})
}

// Note: list endpoints keep their one-line api.authorizeCompany(query) check
// in-handler because they read company_id from the query and also need it for
// filtering — that shared helper is already the single authorization point, not
// the duplicated per-resource logic this file replaces.

// ── context getters ───────────────────────────────────────────────────────────

func (api *API) companyFromCtx(r *http.Request) db.Company {
	c, _ := r.Context().Value(companyKey).(db.Company)
	return c
}
func (api *API) taskFromCtx(r *http.Request) db.Task {
	t, _ := r.Context().Value(taskKey).(db.Task)
	return t
}
func (api *API) runFromCtx(r *http.Request) db.Run {
	run, _ := r.Context().Value(runKey).(db.Run)
	return run
}
func (api *API) agentFromCtx(r *http.Request) db.Agent {
	a, _ := r.Context().Value(agentKey).(db.Agent)
	return a
}
func (api *API) projectFromCtx(r *http.Request) db.Project {
	p, _ := r.Context().Value(projectKey).(db.Project)
	return p
}
func (api *API) skillFromCtx(r *http.Request) db.Skill {
	s, _ := r.Context().Value(skillKey).(db.Skill)
	return s
}
func (api *API) mcpServerFromCtx(r *http.Request) db.MCPServer {
	s, _ := r.Context().Value(mcpServerKey).(db.MCPServer)
	return s
}
func (api *API) providerFromCtx(r *http.Request) db.LLMProvider {
	p, _ := r.Context().Value(providerKey).(db.LLMProvider)
	return p
}
func (api *API) modelGroupFromCtx(r *http.Request) db.ModelGroup {
	g, _ := r.Context().Value(modelGroupKey).(db.ModelGroup)
	return g
}
func (api *API) mcpAccountFromCtx(r *http.Request) db.MCPAccount {
	a, _ := r.Context().Value(mcpAccountKey).(db.MCPAccount)
	return a
}
