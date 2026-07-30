package deploysync

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/crypto/nacl/box"
)

// GitHubTarget pushes entries to one GitHub repository environment
// (Actions environment secrets + variables).
type GitHubTarget struct {
	Token string
	// Repo is "owner/repo".
	Repo string
	// Environment is the repo environment name.
	Environment string
}

// NewGitHubTarget builds a target from connector fields.
func NewGitHubTarget(token, repo, environment string) *GitHubTarget {
	return &GitHubTarget{Token: token, Repo: repo, Environment: environment}
}

func (g *GitHubTarget) base() string { return baseURL("GITHUB_API_URL", "https://api.github.com") }

func (g *GitHubTarget) envPath(rest string) string {
	return fmt.Sprintf("%s/repos/%s/environments/%s%s", g.base(), g.Repo, url.PathEscape(g.Environment), rest)
}

func (g *GitHubTarget) do(ctx context.Context, method, u string, body interface{}) (*http.Response, []byte, error) {
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
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, respBody, nil
}

// environmentPublicKey fetches the env public key used to seal secrets.
func (g *GitHubTarget) environmentPublicKey(ctx context.Context) (keyID string, key [32]byte, err error) {
	resp, body, err := g.do(ctx, http.MethodGet, g.envPath("/secrets/public-key"), nil)
	if err != nil {
		return "", key, fmt.Errorf("github public key: %w", err)
	}
	if resp.StatusCode >= 300 {
		return "", key, httpError("github public key", resp, body)
	}
	var pk struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := json.Unmarshal(body, &pk); err != nil {
		return "", key, fmt.Errorf("github public key: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(pk.Key)
	if err != nil || len(raw) != 32 {
		return "", key, fmt.Errorf("github public key: malformed key")
	}
	copy(key[:], raw)
	return pk.KeyID, key, nil
}

// sealSecret encrypts a secret value for GitHub with libsodium's
// crypto_box_seal (anonymous sealed box), as the API requires.
func sealSecret(value string, recipient [32]byte) (string, error) {
	sealed, err := box.SealAnonymous(nil, []byte(value), &recipient, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// PushAll upserts every entry: secrets are sealed and PUT, variables are
// POSTed (PATCH on name conflict).
func (g *GitHubTarget) PushAll(ctx context.Context, entries []Entry) error {
	var keyID string
	var pubKey [32]byte
	for _, e := range entries {
		if e.Kind == "variable" {
			if err := g.pushVariable(ctx, e); err != nil {
				return err
			}
			continue
		}
		if keyID == "" {
			var err error
			if keyID, pubKey, err = g.environmentPublicKey(ctx); err != nil {
				return err
			}
		}
		if err := g.pushSecret(ctx, e, keyID, pubKey); err != nil {
			return err
		}
	}
	return nil
}

func (g *GitHubTarget) pushSecret(ctx context.Context, e Entry, keyID string, pubKey [32]byte) error {
	sealed, err := sealSecret(e.Value, pubKey)
	if err != nil {
		return fmt.Errorf("github seal secret %s: %w", e.Name, err)
	}
	resp, body, err := g.do(ctx, http.MethodPut, g.envPath("/secrets/"+url.PathEscape(e.Name)), map[string]string{
		"encrypted_value": sealed,
		"key_id":          keyID,
	})
	if err != nil {
		return fmt.Errorf("github push secret %s: %w", e.Name, err)
	}
	if resp.StatusCode >= 300 {
		return httpError("github push secret "+e.Name, resp, body)
	}
	return nil
}

func (g *GitHubTarget) pushVariable(ctx context.Context, e Entry) error {
	payload := map[string]string{"name": e.Name, "value": e.Value}
	resp, body, err := g.do(ctx, http.MethodPost, g.envPath("/variables"), payload)
	if err != nil {
		return fmt.Errorf("github push variable %s: %w", e.Name, err)
	}
	if resp.StatusCode < 300 {
		return nil
	}
	// Name already exists → update in place.
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusUnprocessableEntity {
		resp, body, err = g.do(ctx, http.MethodPatch, g.envPath("/variables/"+url.PathEscape(e.Name)), payload)
		if err != nil {
			return fmt.Errorf("github update variable %s: %w", e.Name, err)
		}
		if resp.StatusCode < 300 {
			return nil
		}
	}
	return httpError("github push variable "+e.Name, resp, body)
}

// Delete removes both the secret and the variable with the given name (the
// entry's kind may have changed since it was pushed; deleting both keeps the
// target consistent). 404s are ignored.
func (g *GitHubTarget) Delete(ctx context.Context, name string) error {
	for _, path := range []string{"/secrets/" + url.PathEscape(name), "/variables/" + url.PathEscape(name)} {
		resp, body, err := g.do(ctx, http.MethodDelete, g.envPath(path), nil)
		if err != nil {
			return fmt.Errorf("github delete %s: %w", name, err)
		}
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
			return httpError("github delete "+name, resp, body)
		}
	}
	return nil
}

// GitHubRepo is one repository visible to the token.
type GitHubRepo struct {
	FullName string `json:"full_name"`
}

// ListGitHubRepos lists the token's repositories for connector setup.
func ListGitHubRepos(ctx context.Context, token string) ([]GitHubRepo, error) {
	g := &GitHubTarget{Token: token}
	u := g.base() + "/user/repos?per_page=100&sort=pushed"
	resp, body, err := g.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("github repos: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, httpError("github repos", resp, body)
	}
	var repos []GitHubRepo
	if err := json.Unmarshal(body, &repos); err != nil {
		return nil, fmt.Errorf("github repos: %w", err)
	}
	return repos, nil
}

// ListGitHubEnvironments lists a repository's environments.
func ListGitHubEnvironments(ctx context.Context, token, repo string) ([]string, error) {
	if !strings.Contains(repo, "/") {
		return nil, fmt.Errorf("github environments: repo must be owner/repo")
	}
	g := &GitHubTarget{Token: token, Repo: repo}
	u := fmt.Sprintf("%s/repos/%s/environments", g.base(), repo)
	resp, body, err := g.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("github environments: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, httpError("github environments", resp, body)
	}
	var out struct {
		Environments []struct {
			Name string `json:"name"`
		} `json:"environments"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("github environments: %w", err)
	}
	names := make([]string, 0, len(out.Environments))
	for _, e := range out.Environments {
		names = append(names, e.Name)
	}
	return names, nil
}
