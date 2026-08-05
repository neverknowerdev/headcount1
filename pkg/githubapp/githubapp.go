// Package githubapp implements the small GitHub App surface Headcount1 needs.
package githubapp

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"agent-orchestrator/db"
)

const apiURL = "https://api.github.com"

type Config struct{ AppID, ClientID, ClientSecret, PrivateKey, Slug, PublicURL, WebhookSecret string }
type Client struct {
	config     Config
	httpClient *http.Client
}
type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}
type Installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
}

func FromEnv() (*Client, error) {
	c := Config{AppID: os.Getenv("HEADCOUNT1_GITHUB_APP_ID"), ClientID: os.Getenv("HEADCOUNT1_GITHUB_APP_CLIENT_ID"), ClientSecret: os.Getenv("HEADCOUNT1_GITHUB_APP_CLIENT_SECRET"), PrivateKey: normalizePrivateKey(os.Getenv("HEADCOUNT1_GITHUB_APP_PRIVATE_KEY")), Slug: os.Getenv("HEADCOUNT1_GITHUB_APP_SLUG"), PublicURL: strings.TrimRight(os.Getenv("DEPLOY_URL"), "/"), WebhookSecret: os.Getenv("HEADCOUNT1_GITHUB_APP_WEBHOOK_SECRET")}
	if c.AppID == "" || c.ClientID == "" || c.ClientSecret == "" || c.PrivateKey == "" {
		return nil, errors.New("GitHub App is not configured")
	}
	if c.Slug == "" {
		return nil, errors.New("HEADCOUNT1_GITHUB_APP_SLUG is required")
	}
	return &Client{config: c, httpClient: &http.Client{Timeout: 20 * time.Second}}, nil
}

// normalizePrivateKey supports both a normal multi-line GitHub Actions secret
// and a value copied from a dotenv-style configuration with literal "\\n"s.
func normalizePrivateKey(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), `\n`, "\n")
}
func (c *Client) Configured() bool { return c != nil }
func (c *Client) InstallURL() string {
	return "https://github.com/apps/" + url.PathEscape(c.config.Slug) + "/installations/new"
}
func (c *Client) AuthorizeURL(state, redirect string, selectAccount ...bool) string {
	q := url.Values{"client_id": {c.config.ClientID}, "state": {state}, "redirect_uri": {redirect}}
	if len(selectAccount) > 0 && selectAccount[0] {
		q.Set("prompt", "select_account")
	}
	return "https://github.com/login/oauth/authorize?" + q.Encode()
}

// ExchangeCode repeats redirectURI when authorization used one. GitHub Apps
// can register multiple callback URLs; omitting it here makes the exchange
// ambiguous and breaks staging/prod flows that share one App.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (string, error) {
	v := url.Values{"client_id": {c.config.ClientID}, "client_secret": {c.config.ClientSecret}, "code": {code}}
	if redirectURI != "" {
		v.Set("redirect_uri", redirectURI)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(v.Encode()))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := c.doJSON(req, &out); err != nil {
		return "", err
	}
	if out.Error != "" || out.AccessToken == "" {
		return "", fmt.Errorf("GitHub authorization failed: %s", out.Error)
	}
	return out.AccessToken, nil
}
func (c *Client) UserInstallations(ctx context.Context, userToken string) ([]Installation, error) {
	var out struct {
		Installations []Installation `json:"installations"`
	}
	err := c.api(ctx, http.MethodGet, "/user/installations", userToken, nil, &out)
	return out.Installations, err
}
func (c *Client) UserRepositories(ctx context.Context, userToken string, installationID int64) ([]Repository, error) {
	var out struct {
		Repositories []Repository `json:"repositories"`
	}
	err := c.api(ctx, http.MethodGet, fmt.Sprintf("/user/installations/%d/repositories", installationID), userToken, nil, &out)
	return out.Repositories, err
}
func (c *Client) InstallationToken(ctx context.Context, installationID int64, repositoryID int64) (string, error) {
	jwt, err := c.appJWT()
	if err != nil {
		return "", err
	}
	body := map[string]any{}
	if repositoryID > 0 {
		body["repository_ids"] = []int64{repositoryID}
	}
	var out struct {
		Token string `json:"token"`
	}
	err = c.api(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", installationID), jwt, body, &out)
	return out.Token, err
}

// TokenForProject creates the short-lived GitHub App installation token used
// for every git network operation on a repository selected through GitHub.
// Projects without GitHub metadata intentionally return an empty token so
// callers retain their configured SSH/manual repository behavior.
func TokenForProject(ctx context.Context, project db.Project) (string, error) {
	if project.GitHubInstallationID == 0 {
		return "", nil
	}
	c, err := FromEnv()
	if err != nil {
		return "", err
	}
	return c.InstallationToken(ctx, project.GitHubInstallationID, project.GitHubRepositoryID)
}
func (c *Client) CreatePullRequest(ctx context.Context, token, ownerRepo, title, head, base, body string) (int, string, error) {
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	err := c.api(ctx, http.MethodPost, "/repos/"+ownerRepo+"/pulls", token, map[string]any{"title": title, "head": head, "base": base, "body": body, "draft": true}, &out)
	return out.Number, out.HTMLURL, err
}
func (c *Client) VerifyWebhook(body []byte, signature string) bool {
	if c == nil || c.config.WebhookSecret == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(c.config.WebhookSecret))
	mac.Write(body)
	want := prefix + fmt.Sprintf("%x", mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(signature)) == 1
}
func (c *Client) appJWT() (string, error) {
	block, _ := pem.Decode([]byte(c.config.PrivateKey))
	if block == nil {
		return "", errors.New("invalid GitHub App private key")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		if parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes); e == nil {
			var ok bool
			key, ok = parsed.(*rsa.PrivateKey)
			if !ok {
				return "", errors.New("GitHub App key is not RSA")
			}
		} else {
			return "", err
		}
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":%s}`, time.Now().Add(-time.Minute).Unix(), time.Now().Add(9*time.Minute).Unix(), c.config.AppID)))
	input := header + "." + claims
	hash := crypto.SHA256.New()
	hash.Write([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash.Sum(nil))
	if err != nil {
		return "", err
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
func (c *Client) api(ctx context.Context, method, path, token string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequestWithContext(ctx, method, apiURL+path, r)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.doJSON(req, out)
}
func (c *Client) doJSON(req *http.Request, out any) error {
	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("GitHub API %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}
