package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/integration/deploysync"
	"agent-orchestrator/pkg/secrets"

	"github.com/go-chi/chi/v5"
)

// Deploy connectors: each project environment can be linked to Vercel project
// envs and/or GitHub repo environments. Every entry change is pushed so the
// next deployment on the target picks up current values. Push-only by design:
// the targets don't allow reading secret values back (GitHub secrets and
// Vercel sensitive vars are write-only), so this system is the source of
// truth.

// connectorTarget builds the deploysync target for a connector, decrypting
// its API token at the point of use.
func connectorTarget(c db.EnvironmentConnector) (deploysync.Target, error) {
	token, err := secrets.Default().Decrypt(c.ApiTokenEncrypted)
	if err != nil {
		return nil, err
	}
	switch c.Provider {
	case db.ConnectorVercel:
		return deploysync.NewVercelTarget(token, c.TargetProject, c.TargetEnvironment, c.TeamID), nil
	case db.ConnectorGitHub:
		return deploysync.NewGitHubTarget(token, c.TargetProject, c.TargetEnvironment), nil
	}
	return nil, errors.New("unknown connector provider")
}

// environmentEntries decrypts an environment's entries for pushing. Secrets
// go through Decrypt (redaction-registered); variables through DecryptRaw.
func (api *API) environmentEntries(ctx context.Context, envID int32) ([]deploysync.Entry, error) {
	rows, err := api.q.ListEnvironmentSecrets(ctx, envID)
	if err != nil {
		return nil, err
	}
	entries := make([]deploysync.Entry, 0, len(rows))
	for _, row := range rows {
		var plain string
		var decErr error
		if row.Kind == db.EnvEntryVariable {
			plain, decErr = secrets.Default().DecryptRaw(row.ValueEncrypted)
		} else {
			plain, decErr = secrets.Default().Decrypt(row.ValueEncrypted)
		}
		if decErr != nil {
			return nil, decErr
		}
		entries = append(entries, deploysync.Entry{Name: row.Name, Value: plain, Kind: row.Kind})
	}
	return entries, nil
}

// pushEnvironment pushes all entries of an environment to every connector,
// recording per-connector status. Errors are recorded, not returned — a
// broken target must not fail the local write.
func (api *API) pushEnvironment(ctx context.Context, envID int32) {
	connectors, err := api.q.ListEnvironmentConnectors(ctx, envID)
	if err != nil || len(connectors) == 0 {
		return
	}
	entries, err := api.environmentEntries(ctx, envID)
	if err != nil {
		for _, c := range connectors {
			_ = api.q.SetConnectorSyncResult(ctx, c.ID, err)
		}
		return
	}
	for _, c := range connectors {
		target, err := connectorTarget(c)
		if err == nil {
			err = target.PushAll(ctx, entries)
		}
		_ = api.q.SetConnectorSyncResult(ctx, c.ID, err)
	}
}

// pushEnvironmentAsync runs the push in the background with its own timeout,
// so the HTTP write that triggered it returns immediately.
func (api *API) pushEnvironmentAsync(envID int32) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		api.pushEnvironment(ctx, envID)
	}()
}

// deleteFromConnectors removes one entry name from every connected target.
func (api *API) deleteFromConnectorsAsync(envID int32, name string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		connectors, err := api.q.ListEnvironmentConnectors(ctx, envID)
		if err != nil {
			return
		}
		for _, c := range connectors {
			target, err := connectorTarget(c)
			if err == nil {
				err = target.Delete(ctx, name)
			}
			_ = api.q.SetConnectorSyncResult(ctx, c.ID, err)
		}
	}()
}

type connectorPayload struct {
	Provider          string `json:"provider"`
	Token             string `json:"token"`
	TargetProject     string `json:"target_project"`
	TargetEnvironment string `json:"target_environment"`
	TeamID            string `json:"team_id"`
}

// CreateEnvironmentConnector links an environment to a deploy target and
// immediately pushes the current entries to it.
func (api *API) CreateEnvironmentConnector(w http.ResponseWriter, r *http.Request) {
	envID, err := strconv.Atoi(chi.URLParam(r, "envID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	if _, err := api.authorizeEnvironment(r, int32(envID)); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	var p connectorPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	uid := api.currentUserID(r)
	if !secrets.IsUnlocked(uid) {
		api.respondError(w, http.StatusConflict, "vault locked — unlock with your passkey before saving a connector")
		return
	}
	conn, err := api.q.CreateEnvironmentConnector(r.Context(), db.EnvironmentConnector{
		EnvironmentID:     int32(envID),
		Provider:          p.Provider,
		TargetProject:     p.TargetProject,
		TargetEnvironment: p.TargetEnvironment,
		TeamID:            p.TeamID,
	}, uid, p.Token)
	if err != nil {
		if errors.Is(err, secrets.ErrLocked) {
			api.respondError(w, http.StatusConflict, "vault locked — unlock with your passkey before saving a connector")
			return
		}
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Bring the target up to date right away.
	api.pushEnvironmentAsync(int32(envID))
	api.respondJSON(w, http.StatusCreated, conn)
}

// DeleteEnvironmentConnector unlinks a deploy target. Values already pushed
// remain on the target.
func (api *API) DeleteEnvironmentConnector(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "connectorID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid connector id")
		return
	}
	conn, err := api.q.GetEnvironmentConnector(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := api.authorizeEnvironment(r, conn.EnvironmentID); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	if err := api.q.DeleteEnvironmentConnector(r.Context(), int32(id)); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SyncEnvironment pushes all entries to every connector, synchronously, and
// returns the per-connector results so the UI can display them immediately.
func (api *API) SyncEnvironment(w http.ResponseWriter, r *http.Request) {
	envID, err := strconv.Atoi(chi.URLParam(r, "envID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	if _, err := api.authorizeEnvironment(r, int32(envID)); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	api.pushEnvironment(ctx, int32(envID))
	connectors, err := api.q.ListEnvironmentConnectors(r.Context(), int32(envID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, connectors)
}

// remoteEntriesResponse aggregates what currently exists on every connected
// target. Names only — secret values are write-only on the targets, so a
// remote-only entry can be displayed and overwritten but never read.
type remoteEntriesResponse struct {
	Entries []deploysync.RemoteEntry `json:"entries"`
	Errors  []remoteConnectorError   `json:"errors,omitempty"`
}

type remoteConnectorError struct {
	ConnectorID int32  `json:"connector_id"`
	Provider    string `json:"provider"`
	Error       string `json:"error"`
}

// ListRemoteEntries pulls the entry names currently set on every connected
// deploy target, live (called each time the environments UI opens). A broken
// connector contributes an error instead of failing the whole listing.
func (api *API) ListRemoteEntries(w http.ResponseWriter, r *http.Request) {
	envID, err := strconv.Atoi(chi.URLParam(r, "envID"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	if _, err := api.authorizeEnvironment(r, int32(envID)); err != nil {
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	connectors, err := api.q.ListEnvironmentConnectors(r.Context(), int32(envID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp := remoteEntriesResponse{Entries: []deploysync.RemoteEntry{}}
	seen := map[string]bool{}
	for _, c := range connectors {
		target, terr := connectorTarget(c)
		var remote []deploysync.RemoteEntry
		if terr == nil {
			remote, terr = target.ListRemote(ctx)
		}
		if terr != nil {
			resp.Errors = append(resp.Errors, remoteConnectorError{ConnectorID: c.ID, Provider: c.Provider, Error: terr.Error()})
			continue
		}
		for _, e := range remote {
			if seen[e.Name] {
				continue
			}
			seen[e.Name] = true
			resp.Entries = append(resp.Entries, e)
		}
	}
	api.respondJSON(w, http.StatusOK, resp)
}

type discoveryPayload struct {
	Provider string `json:"provider"`
	Token    string `json:"token"`
	TeamID   string `json:"team_id"`
	// Project narrows discovery to one project/repo's environments.
	Project string `json:"project"`
}

// DiscoverConnectorTargets lists what a token can reach, for the connector
// setup wizard. Without "project" it returns the projects/repos; with it, the
// environments of that project/repo. The token travels in the request body
// only — it is never stored by this endpoint and never appears in responses.
func (api *API) DiscoverConnectorTargets(w http.ResponseWriter, r *http.Request) {
	var p discoveryPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Token == "" {
		api.respondError(w, http.StatusBadRequest, "token is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	switch p.Provider {
	case db.ConnectorVercel:
		if p.Project == "" {
			projects, err := deploysync.ListVercelProjects(ctx, p.Token, p.TeamID)
			if err != nil {
				api.respondError(w, http.StatusBadGateway, err.Error())
				return
			}
			api.respondJSON(w, http.StatusOK, map[string]interface{}{"projects": projects})
			return
		}
		envs, err := deploysync.ListVercelEnvironments(ctx, p.Token, p.TeamID, p.Project)
		if err != nil {
			api.respondError(w, http.StatusBadGateway, err.Error())
			return
		}
		api.respondJSON(w, http.StatusOK, map[string]interface{}{"environments": envs})
	case db.ConnectorGitHub:
		if p.Project == "" {
			repos, err := deploysync.ListGitHubRepos(ctx, p.Token)
			if err != nil {
				api.respondError(w, http.StatusBadGateway, err.Error())
				return
			}
			out := make([]map[string]string, 0, len(repos))
			for _, repo := range repos {
				out = append(out, map[string]string{"id": repo.FullName, "name": repo.FullName})
			}
			api.respondJSON(w, http.StatusOK, map[string]interface{}{"projects": out})
			return
		}
		names, err := deploysync.ListGitHubEnvironments(ctx, p.Token, p.Project)
		if err != nil {
			api.respondError(w, http.StatusBadGateway, err.Error())
			return
		}
		envs := make([]map[string]string, 0, len(names))
		for _, n := range names {
			envs = append(envs, map[string]string{"id": n, "name": n})
		}
		api.respondJSON(w, http.StatusOK, map[string]interface{}{"environments": envs})
	default:
		api.respondError(w, http.StatusBadRequest, "provider must be vercel or github")
	}
}
