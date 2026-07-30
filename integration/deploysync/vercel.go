package deploysync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// VercelTarget pushes entries to one Vercel project environment.
type VercelTarget struct {
	Token string
	// Project is the Vercel project id or name.
	Project string
	// Environment is a standard target (production / preview / development)
	// or a custom environment id ("env_*").
	Environment string
	// TeamID scopes team resources; empty for a personal account.
	TeamID string
}

// NewVercelTarget builds a target from connector fields.
func NewVercelTarget(token, project, environment, teamID string) *VercelTarget {
	return &VercelTarget{Token: token, Project: project, Environment: environment, TeamID: teamID}
}

func (v *VercelTarget) base() string { return baseURL("VERCEL_API_URL", "https://api.vercel.com") }

func (v *VercelTarget) url(path string, query url.Values) string {
	if query == nil {
		query = url.Values{}
	}
	if v.TeamID != "" {
		query.Set("teamId", v.TeamID)
	}
	u := v.base() + path
	if enc := query.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

func (v *VercelTarget) do(ctx context.Context, method, u string, body interface{}) (*http.Response, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+v.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, respBody, nil
}

// isCustomEnv reports whether the configured environment is a Vercel custom
// environment id rather than a standard target.
func (v *VercelTarget) isCustomEnv() bool { return strings.HasPrefix(v.Environment, "env_") }

// vercelType maps our entry kind to the Vercel env var type. Secrets become
// "sensitive" (write-only on Vercel, their secret equivalent); Vercel rejects
// sensitive vars on the development target, so those fall back to "encrypted"
// (encrypted at rest, readable by the team). Variables are "plain".
func (v *VercelTarget) vercelType(kind string) string {
	if kind == "variable" {
		return "plain"
	}
	if v.Environment == "development" {
		return "encrypted"
	}
	return "sensitive"
}

type vercelEnvItem struct {
	Key                  string   `json:"key"`
	Value                string   `json:"value"`
	Type                 string   `json:"type"`
	Target               []string `json:"target,omitempty"`
	CustomEnvironmentIDs []string `json:"customEnvironmentIds,omitempty"`
}

// PushAll upserts every entry via POST /v10/projects/{id}/env?upsert=true.
func (v *VercelTarget) PushAll(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	items := make([]vercelEnvItem, 0, len(entries))
	for _, e := range entries {
		item := vercelEnvItem{Key: e.Name, Value: e.Value, Type: v.vercelType(e.Kind)}
		if v.isCustomEnv() {
			item.CustomEnvironmentIDs = []string{v.Environment}
		} else {
			item.Target = []string{v.Environment}
		}
		items = append(items, item)
	}
	q := url.Values{}
	q.Set("upsert", "true")
	u := v.url("/v10/projects/"+url.PathEscape(v.Project)+"/env", q)
	resp, body, err := v.do(ctx, http.MethodPost, u, items)
	if err != nil {
		return fmt.Errorf("vercel push: %w", err)
	}
	if resp.StatusCode >= 300 {
		return httpError("vercel push", resp, body)
	}
	// The endpoint reports per-item failures without a non-2xx status.
	var result struct {
		Failed []struct {
			Error struct {
				Key     string `json:"key"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"failed"`
	}
	if json.Unmarshal(body, &result) == nil && len(result.Failed) > 0 {
		f := result.Failed[0]
		return fmt.Errorf("vercel push: %d item(s) failed, first: %s: %s", len(result.Failed), f.Error.Key, f.Error.Message)
	}
	return nil
}

// Delete removes the entry with the given key from the configured
// environment. Vercel deletes by env var ID, so it lists first.
func (v *VercelTarget) Delete(ctx context.Context, name string) error {
	u := v.url("/v10/projects/"+url.PathEscape(v.Project)+"/env", nil)
	resp, body, err := v.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("vercel list: %w", err)
	}
	if resp.StatusCode >= 300 {
		return httpError("vercel list", resp, body)
	}
	var list struct {
		Envs []struct {
			ID                   string   `json:"id"`
			Key                  string   `json:"key"`
			Target               []string `json:"target"`
			CustomEnvironmentIDs []string `json:"customEnvironmentIds"`
		} `json:"envs"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("vercel list: %w", err)
	}
	for _, e := range list.Envs {
		if e.Key != name || !v.matchesEnv(e.Target, e.CustomEnvironmentIDs) {
			continue
		}
		du := v.url("/v9/projects/"+url.PathEscape(v.Project)+"/env/"+url.PathEscape(e.ID), nil)
		resp, body, err := v.do(ctx, http.MethodDelete, du, nil)
		if err != nil {
			return fmt.Errorf("vercel delete: %w", err)
		}
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
			return httpError("vercel delete", resp, body)
		}
	}
	return nil
}

func (v *VercelTarget) matchesEnv(targets, customIDs []string) bool {
	if v.isCustomEnv() {
		for _, id := range customIDs {
			if id == v.Environment {
				return true
			}
		}
		return false
	}
	for _, t := range targets {
		if t == v.Environment {
			return true
		}
	}
	return false
}

// VercelProject is one project visible to the token.
type VercelProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListVercelProjects lists projects for connector setup.
func ListVercelProjects(ctx context.Context, token, teamID string) ([]VercelProject, error) {
	v := &VercelTarget{Token: token, TeamID: teamID}
	q := url.Values{}
	q.Set("limit", "100")
	resp, body, err := v.do(ctx, http.MethodGet, v.url("/v10/projects", q), nil)
	if err != nil {
		return nil, fmt.Errorf("vercel projects: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, httpError("vercel projects", resp, body)
	}
	var out struct {
		Projects []VercelProject `json:"projects"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("vercel projects: %w", err)
	}
	return out.Projects, nil
}

// VercelEnvironment is one environment choice for a project: the three
// standard targets plus any custom environments.
type VercelEnvironment struct {
	ID   string `json:"id"`   // target name or custom env id
	Name string `json:"name"` // display name
}

// ListVercelEnvironments returns the environment options of a project.
func ListVercelEnvironments(ctx context.Context, token, teamID, project string) ([]VercelEnvironment, error) {
	envs := []VercelEnvironment{
		{ID: "production", Name: "production"},
		{ID: "preview", Name: "preview"},
		{ID: "development", Name: "development"},
	}
	v := &VercelTarget{Token: token, TeamID: teamID, Project: project}
	resp, body, err := v.do(ctx, http.MethodGet, v.url("/v9/projects/"+url.PathEscape(project)+"/custom-environments", nil), nil)
	if err != nil {
		return nil, fmt.Errorf("vercel custom environments: %w", err)
	}
	// Custom environments may be unavailable on the plan — the standard
	// targets still apply.
	if resp.StatusCode < 300 {
		var out struct {
			Environments []struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
			} `json:"environments"`
		}
		if json.Unmarshal(body, &out) == nil {
			for _, ce := range out.Environments {
				envs = append(envs, VercelEnvironment{ID: ce.ID, Name: ce.Slug + " (custom)"})
			}
		}
	}
	return envs, nil
}
