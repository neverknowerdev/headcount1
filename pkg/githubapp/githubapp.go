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

type Config struct {
	AppID         string
	ClientID      string
	ClientSecret  string
	PrivateKey    string
	Slug          string
	WebhookSecret string
}

type AuthorizeOptions struct {
	SelectAccount bool
}

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

type InstallationAccount struct {
	Login string `json:"login"`
}

type Installation struct {
	ID      int64               `json:"id"`
	Account InstallationAccount `json:"account"`
}

// User is the stable GitHub identity that authorized an OAuth token. It must
// not be inferred from an installation owner because organisation installs can
// be shared by multiple people.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

func FromEnv() (*Client, error) {
	config := Config{
		AppID:         os.Getenv("HEADCOUNT1_GITHUB_APP_ID"),
		ClientID:      os.Getenv("HEADCOUNT1_GITHUB_APP_CLIENT_ID"),
		ClientSecret:  os.Getenv("HEADCOUNT1_GITHUB_APP_CLIENT_SECRET"),
		PrivateKey:    normalizePrivateKey(os.Getenv("HEADCOUNT1_GITHUB_APP_PRIVATE_KEY")),
		Slug:          os.Getenv("HEADCOUNT1_GITHUB_APP_SLUG"),
		WebhookSecret: os.Getenv("HEADCOUNT1_GITHUB_APP_WEBHOOK_SECRET"),
	}
	if config.AppID == "" || config.ClientID == "" || config.ClientSecret == "" || config.PrivateKey == "" {
		return nil, errors.New("GitHub App is not configured")
	}
	if config.Slug == "" {
		return nil, errors.New("HEADCOUNT1_GITHUB_APP_SLUG is required")
	}
	return NewClient(config, &http.Client{Timeout: 20 * time.Second}), nil
}

func NewClient(config Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{config: config, httpClient: httpClient}
}

// normalizePrivateKey supports both a normal multi-line GitHub Actions secret
// and a value copied from a dotenv-style configuration with literal "\\n"s.
func normalizePrivateKey(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), `\n`, "\n")
}

func (c *Client) InstallURL() string {
	return "https://github.com/apps/" + url.PathEscape(c.config.Slug) + "/installations/new"
}
func (c *Client) AuthorizeURL(state, redirect string, options AuthorizeOptions) string {
	query := url.Values{
		"client_id":    {c.config.ClientID},
		"state":        {state},
		"redirect_uri": {redirect},
	}
	if options.SelectAccount {
		query.Set("prompt", "select_account")
	}
	return "https://github.com/login/oauth/authorize?" + query.Encode()
}

func (c *Client) User(ctx context.Context, userToken string) (User, error) {
	var out User
	if err := c.api(ctx, http.MethodGet, "/user", userToken, nil, &out); err != nil {
		return User{}, err
	}
	if out.ID == 0 || out.Login == "" {
		return User{}, errors.New("GitHub returned an invalid user identity")
	}
	return out, nil
}

// ExchangeCode repeats redirectURI when authorization used one. GitHub Apps
// can register multiple callback URLs; omitting it here makes the exchange
// ambiguous and breaks staging/prod flows that share one App.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{
		"client_id":     {c.config.ClientID},
		"client_secret": {c.config.ClientSecret},
		"code":          {code},
	}
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create GitHub token request: %w", err)
	}
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
	var installations []Installation
	err := c.getPaginated(ctx, "/user/installations", userToken, func(body io.Reader) error {
		var page struct {
			Installations []Installation `json:"installations"`
		}
		if err := json.NewDecoder(body).Decode(&page); err != nil {
			return err
		}
		installations = append(installations, page.Installations...)
		return nil
	})
	return installations, err
}
func (c *Client) UserRepositories(ctx context.Context, userToken string, installationID int64) ([]Repository, error) {
	var repositories []Repository
	err := c.getPaginated(ctx, fmt.Sprintf("/user/installations/%d/repositories", installationID), userToken, func(body io.Reader) error {
		var page struct {
			Repositories []Repository `json:"repositories"`
		}
		if err := json.NewDecoder(body).Decode(&page); err != nil {
			return err
		}
		repositories = append(repositories, page.Repositories...)
		return nil
	})
	return repositories, err
}

// InstallationRepositories lists repositories visible to one GitHub App
// installation. Unlike UserRepositories it uses an installation token, so it
// remains valid when the one-time user OAuth grant is no longer usable.
func (c *Client) InstallationRepositories(ctx context.Context, installationToken string) ([]Repository, error) {
	var repositories []Repository
	err := c.getPaginated(ctx, "/installation/repositories", installationToken, func(body io.Reader) error {
		var page struct {
			Repositories []Repository `json:"repositories"`
		}
		if err := json.NewDecoder(body).Decode(&page); err != nil {
			return err
		}
		repositories = append(repositories, page.Repositories...)
		return nil
	})
	return repositories, err
}

// getPaginated follows GitHub's Link: rel="next" cursor so repository
// discovery never silently drops accounts or repositories after the first
// page. The callback receives a single JSON page at a time.
func (c *Client) getPaginated(ctx context.Context, endpoint, token string, consume func(io.Reader) error) error {
	next, err := url.Parse(apiURL + endpoint)
	if err != nil {
		return err
	}
	query := next.Query()
	if query.Get("per_page") == "" {
		query.Set("per_page", "100")
		next.RawQuery = query.Encode()
	}
	for next != nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next.String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}
		if res.StatusCode < http.StatusOK || res.StatusCode > 299 {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			_ = res.Body.Close()
			return fmt.Errorf("GitHub API %s: %s", res.Status, strings.TrimSpace(string(body)))
		}
		consumeErr := consume(res.Body)
		link := res.Header.Get("Link")
		_ = res.Body.Close()
		if consumeErr != nil {
			return consumeErr
		}
		next, err = nextGitHubLink(link)
		if err != nil {
			return err
		}
	}
	return nil
}

func nextGitHubLink(header string) (*url.URL, error) {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start, end := strings.Index(part, "<"), strings.Index(part, ">")
		if start < 0 || end <= start+1 {
			return nil, errors.New("invalid GitHub pagination link")
		}
		return url.Parse(part[start+1 : end])
	}
	return nil, nil
}
func (c *Client) InstallationToken(ctx context.Context, installationID, repositoryID int64) (string, error) {
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
	if err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("GitHub returned an empty installation token")
	}
	return out.Token, nil
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

// TokenForInstallation mints a new short-lived installation token. It is
// deliberately never persisted: callers can request a fresh token after a
// deployment or when a long-lived MCP process reports that its previous token
// has expired.
func TokenForInstallation(ctx context.Context, installationID int64) (string, error) {
	if installationID == 0 {
		return "", errors.New("GitHub installation is not configured")
	}
	c, err := FromEnv()
	if err != nil {
		return "", err
	}
	return c.InstallationToken(ctx, installationID, 0)
}

// TokenForMCPAccount returns an App installation token for GitHub MCP tools.
// The saved OAuth token remains only an identity/repository-selection grant;
// it is not a runtime credential for agents. Project tasks use the exact
// selected repository installation, while general MCP use selects one of the
// account's permitted installations.
func TokenForMCPAccount(ctx context.Context, q *db.Queries, account db.MCPAccount, project *db.Project) (string, error) {
	if project != nil && project.GitHubInstallationID != 0 {
		if account.UserID == nil || *account.UserID == 0 {
			return "", errors.New("GitHub account has no owning user")
		}
		if _, err := q.GetGitHubConnectionForAccountInstallation(ctx, account.ID, *account.UserID, project.GitHubInstallationID); err != nil {
			return "", fmt.Errorf("project installation is not linked to this GitHub account: %w", err)
		}
		return TokenForProject(ctx, *project)
	}
	if account.UserID == nil || *account.UserID == 0 {
		return "", errors.New("GitHub account has no owning user")
	}
	connection, err := q.GetGitHubConnectionForAccount(ctx, account.ID, *account.UserID)
	if err != nil {
		return "", fmt.Errorf("load GitHub App installation for account: %w", err)
	}
	return TokenForInstallation(ctx, connection.InstallationID)
}

func RepositorySlug(repositoryURL string) (string, error) {
	const githubHTTPSPrefix = "https://github.com/"
	if !strings.HasPrefix(repositoryURL, githubHTTPSPrefix) {
		return "", fmt.Errorf("repository URL is not a GitHub HTTPS URL")
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(repositoryURL, githubHTTPSPrefix), ".git")
	if len(strings.Split(slug, "/")) != 2 {
		return "", fmt.Errorf("invalid GitHub repository URL")
	}
	return slug, nil
}

func (c *Client) FindOpenPullRequestByHead(ctx context.Context, token, repositorySlug, head string) (PullRequest, bool, error) {
	query := url.Values{"state": {"open"}, "head": {head}}
	var pulls []PullRequest
	if err := c.api(ctx, http.MethodGet, "/repos/"+repositorySlug+"/pulls?"+query.Encode(), token, nil, &pulls); err != nil {
		return PullRequest{}, false, err
	}
	if len(pulls) == 0 {
		return PullRequest{}, false, nil
	}
	return pulls[0], true, nil
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
	_, _ = mac.Write(body)
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
	_, _ = hash.Write([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash.Sum(nil))
	if err != nil {
		return "", err
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (c *Client) api(ctx context.Context, method, path, token string, body any, out any) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode GitHub API request: %w", err)
		}
		requestBody = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, apiURL+path, requestBody)
	if err != nil {
		return fmt.Errorf("create GitHub API request: %w", err)
	}
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
	if res.StatusCode < http.StatusOK || res.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("GitHub API %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("decode GitHub API response: %w", err)
	}
	return nil
}
