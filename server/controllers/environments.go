package endpoints

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// Environments give tasks a named set of secrets ("headcount1 cloud" /
// "preview" / "production" + user-defined). Secrets are write-only through
// this API: requests carry plaintext once, responses only ever carry names
// and has_value flags — the value is sealed under the caller's DEK and can
// never be read back out.

// authorizeEnvironment returns the environment iff its company is visible to
// the ctx user. Ownership mismatches are 404 like every other resource.
func (api *API) authorizeEnvironment(r *http.Request, envID int32) (db.Environment, error) {
	env, err := api.q.GetEnvironment(r.Context(), envID)
	if err != nil {
		return db.Environment{}, err
	}
	if _, err := api.authorizeCompany(r, env.CompanyID); err != nil {
		return db.Environment{}, err
	}
	return env, nil
}

// systemSecret is one headcount1-managed credential shown (read-only) in the
// default environment: provider API keys, MCP tokens, the git SSH key. They
// are managed on their own pages and used server-side by reference — they are
// NOT injected into the agent shell.
type systemSecret struct {
	Kind     string `json:"kind"` // "llm_provider" | "mcp_account" | "ssh_key"
	Name     string `json:"name"`
	HasValue bool   `json:"has_value"`
}

type environmentResponse struct {
	db.Environment
	Secrets       []db.EnvironmentSecret    `json:"secrets"`
	SystemSecrets []systemSecret            `json:"system_secrets,omitempty"`
	Connectors    []db.EnvironmentConnector `json:"connectors,omitempty"`
}

// ListEnvironments returns a company's environments with their secret NAMES
// (never values). The default environment additionally carries the caller's
// system-managed credentials as a separate read-only group.
func (api *API) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	companyID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid company id")
		return
	}
	if _, err := api.authorizeCompany(r, int32(companyID)); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	envs, err := api.q.ListEnvironments(r.Context(), int32(companyID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]environmentResponse, 0, len(envs))
	for _, env := range envs {
		rows, err := api.q.ListEnvironmentSecrets(r.Context(), env.ID)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := environmentResponse{Environment: env, Secrets: rows}
		if env.IsDefault {
			resp.SystemSecrets = api.systemSecretsForUser(r)
		}
		out = append(out, resp)
	}
	api.respondJSON(w, http.StatusOK, out)
}

// systemSecretsForUser lists the caller's headcount1-managed credentials as
// name + has_value only.
func (api *API) systemSecretsForUser(r *http.Request) []systemSecret {
	uid := api.currentUserID(r)
	var out []systemSecret
	if providers, err := api.q.ListLLMProvidersForUser(r.Context(), uid); err == nil {
		for _, p := range providers {
			if p.ApiKeyEncrypted == "" {
				continue
			}
			out = append(out, systemSecret{Kind: "llm_provider", Name: p.Name + " API key", HasValue: true})
		}
	}
	var accounts []db.MCPAccount
	if err := api.db.WithContext(r.Context()).
		Where("user_id = ?", uid).Find(&accounts).Error; err == nil {
		for _, a := range accounts {
			if a.AuthTokenEncrypted == "" {
				continue
			}
			out = append(out, systemSecret{Kind: "mcp_account", Name: "MCP token: " + a.Name, HasValue: true})
		}
	}
	if cred, err := api.q.GetUserGitCredential(r.Context(), uid); err == nil && cred.SSHPrivateKeyEncrypted != "" {
		out = append(out, systemSecret{Kind: "ssh_key", Name: "Git SSH key", HasValue: true})
	}
	return out
}

type environmentPayload struct {
	Name string `json:"name"`
}

// ListProjectEnvironments returns a project's deploy environments with their
// entries (names only) and connectors.
func (api *API) ListProjectEnvironments(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if _, err := api.authorizeProject(r, int32(projectID)); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	envs, err := api.q.ListProjectEnvironments(r.Context(), int32(projectID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]environmentResponse, 0, len(envs))
	for _, env := range envs {
		rows, err := api.q.ListEnvironmentSecrets(r.Context(), env.ID)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		connectors, err := api.q.ListEnvironmentConnectors(r.Context(), env.ID)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, environmentResponse{Environment: env, Secrets: rows, Connectors: connectors})
	}
	api.respondJSON(w, http.StatusOK, out)
}

// CreateProjectEnvironment adds a deploy environment to a project.
func (api *API) CreateProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	project, err := api.authorizeProject(r, int32(projectID))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	var p environmentPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || strings.TrimSpace(p.Name) == "" {
		api.respondError(w, http.StatusBadRequest, "name is required")
		return
	}
	env, err := api.q.CreateProjectEnvironment(r.Context(), project.CompanyID, project.ID, p.Name)
	if err != nil {
		api.respondError(w, http.StatusConflict, err.Error())
		return
	}
	api.respondJSON(w, http.StatusCreated, environmentResponse{Environment: env, Secrets: []db.EnvironmentSecret{}})
}

// CreateEnvironment adds a user-defined environment to a company.
func (api *API) CreateEnvironment(w http.ResponseWriter, r *http.Request) {
	companyID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid company id")
		return
	}
	if _, err := api.authorizeCompany(r, int32(companyID)); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	var p environmentPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || strings.TrimSpace(p.Name) == "" {
		api.respondError(w, http.StatusBadRequest, "name is required")
		return
	}
	env, err := api.q.CreateEnvironment(r.Context(), int32(companyID), p.Name)
	if err != nil {
		api.respondError(w, http.StatusConflict, err.Error())
		return
	}
	api.respondJSON(w, http.StatusCreated, environmentResponse{Environment: env, Secrets: []db.EnvironmentSecret{}})
}

// UpdateEnvironment renames a user-defined environment.
func (api *API) UpdateEnvironment(w http.ResponseWriter, r *http.Request) {
	envID, err := strconv.Atoi(chi.URLParam(r, "envID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	if _, err := api.authorizeEnvironment(r, int32(envID)); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	var p environmentPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	env, err := api.q.RenameEnvironment(r.Context(), int32(envID), p.Name)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, env)
}

// DeleteEnvironment removes a user-defined environment (tasks referencing it
// fall back to the company default).
func (api *API) DeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	envID, err := strconv.Atoi(chi.URLParam(r, "envID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	if _, err := api.authorizeEnvironment(r, int32(envID)); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	if err := api.q.DeleteEnvironment(r.Context(), int32(envID)); err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type environmentSecretPayload struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Kind is "secret" (default) or "variable". Both are sealed at rest;
	// kind controls redaction and how connectors push to deploy targets.
	Kind string `json:"kind"`
}

// UpsertEnvironmentSecret creates or replaces one named secret. The value is
// sealed under the caller's DEK at write; requires an unlocked vault.
func (api *API) UpsertEnvironmentSecret(w http.ResponseWriter, r *http.Request) {
	envID, err := strconv.Atoi(chi.URLParam(r, "envID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	if _, err := api.authorizeEnvironment(r, int32(envID)); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	var p environmentSecretPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(p.Value) == "" {
		api.respondError(w, http.StatusBadRequest, "value is required")
		return
	}
	uid := api.currentUserID(r)
	if !secrets.IsUnlocked(uid) {
		api.respondError(w, http.StatusConflict, "vault locked — unlock with your passkey before saving a secret")
		return
	}
	row, err := api.q.UpsertEnvironmentSecret(r.Context(), int32(envID), uid, p.Name, p.Value, p.Kind)
	if err != nil {
		if errors.Is(err, secrets.ErrLocked) {
			api.respondError(w, http.StatusConflict, "vault locked — unlock with your passkey before saving a secret")
			return
		}
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Keep connected deploy targets current: the next deployment there must
	// see this value.
	api.pushEnvironmentAsync(int32(envID))
	api.respondJSON(w, http.StatusOK, row)
}

// DeleteEnvironmentSecret removes one named secret from an environment.
func (api *API) DeleteEnvironmentSecret(w http.ResponseWriter, r *http.Request) {
	envID, err := strconv.Atoi(chi.URLParam(r, "envID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	if _, err := api.authorizeEnvironment(r, int32(envID)); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		api.respondError(w, http.StatusBadRequest, "secret name is required")
		return
	}
	if err := api.q.DeleteEnvironmentSecret(r.Context(), int32(envID), name); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			api.respondError(w, http.StatusNotFound, "not found")
			return
		}
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Remove it from connected deploy targets too.
	api.deleteFromConnectorsAsync(int32(envID), name)
	w.WriteHeader(http.StatusNoContent)
}
