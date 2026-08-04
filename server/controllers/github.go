package endpoints

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/githubapp"
	"agent-orchestrator/pkg/secrets"
	"github.com/go-chi/chi/v5"
)

func githubClient() (*githubapp.Client, error) { return githubapp.FromEnv() }
func deploymentURL() string                    { return strings.TrimRight(os.Getenv("DEPLOY_URL"), "/") }

const forwardedWebhookSignatureHeader = "X-Headcount1-Webhook-Forward-Signature"

// A GitHub App has one webhook URL. Production can optionally forward its
// verified deliveries to staging with a second shared secret, allowing both
// environments to use the same App without exposing an unsigned relay.
func forwardedWebhookSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func validForwardedWebhook(body []byte, signature, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}
	return hmac.Equal([]byte(signature), []byte(forwardedWebhookSignature(body, secret)))
}

func forwardGitHubWebhook(body []byte, event string) {
	url, secret := os.Getenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_URL"), os.Getenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET")
	if url == "" || secret == "" {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set(forwardedWebhookSignatureHeader, forwardedWebhookSignature(body, secret))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func (api *API) GitHubStatus(w http.ResponseWriter, r *http.Request) {
	c, err := githubClient()
	if err != nil {
		api.respondJSON(w, http.StatusOK, map[string]any{"configured": false, "error": err.Error()})
		return
	}
	var connections []db.GitHubConnection
	api.db.Where("user_id = ?", api.currentUserID(r)).Order("id").Find(&connections)
	api.respondJSON(w, http.StatusOK, map[string]any{"configured": true, "install_url": c.InstallURL(), "connections": connections})
}

// StartMCPGitHubOAuth starts GitHub App OAuth for a named MCP account. The
// resulting OAuth token is encrypted in that account; it is never shown in the
// UI or entered manually by the user.
func (api *API) StartMCPGitHubOAuth(w http.ResponseWriter, r *http.Request) {
	server := api.mcpServerFromCtx(r)
	if server.Name != "github" {
		api.respondError(w, http.StatusBadRequest, "GitHub OAuth is only available for the GitHub integration")
		return
	}
	c, err := githubClient()
	if err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if deploymentURL() == "" {
		api.respondError(w, http.StatusInternalServerError, "DEPLOY_URL is required for GitHub OAuth")
		return
	}
	var input struct {
		Name       string `json:"name"`
		ReturnPath string `json:"return_path"`
		AccountID  int32  `json:"account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if input.Name == "" {
		input.Name = "GitHub account"
	}
	if input.ReturnPath == "" || !strings.HasPrefix(input.ReturnPath, "/") || strings.HasPrefix(input.ReturnPath, "//") {
		input.ReturnPath = "/settings"
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	callback := deploymentURL() + "/api/github/callback"
	api.db.Create(&db.GitHubOAuthState{ID: state, RedirectURL: callback, MCPServerID: server.ID, MCPAccountID: input.AccountID, UserID: api.currentUserID(r), AccountName: input.Name, ReturnPath: input.ReturnPath, ExpiresAt: time.Now().Add(10 * time.Minute)})
	api.respondJSON(w, http.StatusOK, map[string]string{"authorize_url": c.AuthorizeURL(state, callback), "install_url": c.InstallURL()})
}
func (api *API) StartGitHubOAuth(w http.ResponseWriter, r *http.Request) {
	c, err := githubClient()
	if err != nil {
		api.respondError(w, 400, err.Error())
		return
	}
	callback := deploymentURL() + "/api/github/callback"
	if deploymentURL() == "" {
		api.respondError(w, 500, "DEPLOY_URL is required for GitHub OAuth")
		return
	}
	b := make([]byte, 32)
	rand.Read(b)
	state := hex.EncodeToString(b)
	api.db.Create(&db.GitHubOAuthState{ID: state, RedirectURL: callback, ExpiresAt: time.Now().Add(10 * time.Minute)})
	api.respondJSON(w, http.StatusOK, map[string]string{"authorize_url": c.AuthorizeURL(state, callback), "install_url": c.InstallURL()})
}
func (api *API) GitHubCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	var s db.GitHubOAuthState
	if state == "" || api.db.First(&s, "id = ?", state).Error != nil || s.UsedAt != nil || time.Now().After(s.ExpiresAt) {
		http.Error(w, "GitHub authorization has expired. Return to Headcount1 and try again.", http.StatusBadRequest)
		return
	}
	now := time.Now()
	api.db.Model(&s).Update("used_at", now)
	c, err := githubClient()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	token, err := c.ExchangeCode(r.Context(), code, s.RedirectURL)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	installs, err := c.UserInstallations(r.Context(), token)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if s.MCPServerID != 0 {
		// GitHub may immediately approve an already-authorized installation and
		// redirect back without showing a confirmation screen. Do not create a
		// second MCP account for the same GitHub identity in that case.
		if s.MCPAccountID == 0 {
			var existing db.GitHubConnection
			for _, in := range installs {
				if api.db.Where("user_id = ? AND account_login = ?", s.UserID, in.Account.Login).First(&existing).Error == nil {
					http.Redirect(w, r, deploymentURL()+s.ReturnPath+"?github=already_connected", http.StatusFound)
					return
				}
			}
		}
		sealed, sealErr := secrets.Default().EncryptForUser(s.UserID, token)
		if sealErr != nil {
			http.Error(w, "Your secure vault is locked. Return to Headcount1, unlock it, and try again.", http.StatusConflict)
			return
		}
		var account db.MCPAccount
		if s.MCPAccountID != 0 {
			if api.db.Where("id = ? AND mcp_server_id = ? AND user_id = ?", s.MCPAccountID, s.MCPServerID, s.UserID).First(&account).Error != nil {
				http.Error(w, "GitHub account was not found. Return to Headcount1 and try again.", http.StatusNotFound)
				return
			}
			account.Name = s.AccountName
			account.AuthTokenEncrypted = sealed
			account.LastError = ""
			if _, updateErr := api.q.UpdateMCPAccount(r.Context(), account); updateErr != nil {
				http.Error(w, updateErr.Error(), http.StatusInternalServerError)
				return
			}
			api.db.Where("mcp_account_id = ?", account.ID).Delete(&db.GitHubConnection{})
		} else {
			var createErr error
			account, createErr = api.q.CreateMCPAccount(r.Context(), db.MCPAccount{MCPServerID: s.MCPServerID, Name: s.AccountName, AuthTokenEncrypted: sealed, UserID: &s.UserID})
			if createErr != nil {
				http.Error(w, createErr.Error(), http.StatusInternalServerError)
				return
			}
		}
		for _, in := range installs {
			conn := db.GitHubConnection{InstallationID: in.ID, MCPAccountID: account.ID, UserID: s.UserID, AccountLogin: in.Account.Login, ConnectedAt: now}
			api.db.Where("mcp_account_id = ? AND installation_id = ?", account.ID, in.ID).Assign(conn).FirstOrCreate(&conn)
		}
		http.Redirect(w, r, deploymentURL()+s.ReturnPath+"?github=connected", http.StatusFound)
		return
	}
	for _, in := range installs {
		conn := db.GitHubConnection{InstallationID: in.ID, AccountLogin: in.Account.Login, UserAccessToken: token, ConnectedAt: now}
		api.db.Where("installation_id = ?", in.ID).Assign(conn).FirstOrCreate(&conn)
	}
	http.Redirect(w, r, deploymentURL()+"/settings?github=connected", http.StatusFound)
}
func (api *API) ListGitHubRepositories(w http.ResponseWriter, r *http.Request) {
	c, err := githubClient()
	if err != nil {
		api.respondError(w, 400, err.Error())
		return
	}
	userID := api.currentUserID(r)
	repos := []map[string]any{}
	// Discover installations on every request rather than relying on the list
	// captured during OAuth. A user can change the GitHub App's repository
	// selection later, and the project picker must see that change immediately.
	var accounts []db.MCPAccount
	api.db.Joins("JOIN mcp_servers ON mcp_servers.id = mcp_accounts.mcp_server_id").
		Where("mcp_accounts.user_id = ? AND mcp_servers.name = ?", userID, "github").Find(&accounts)
	seen := map[int64]bool{}
	for _, account := range accounts {
		token, decryptErr := secrets.Default().Decrypt(account.AuthTokenEncrypted)
		if decryptErr != nil || token == "" {
			continue
		}
		installs, installationsErr := c.UserInstallations(r.Context(), token)
		if installationsErr != nil {
			continue
		}
		for _, installation := range installs {
			conn := db.GitHubConnection{InstallationID: installation.ID, MCPAccountID: account.ID, UserID: userID, AccountLogin: installation.Account.Login, ConnectedAt: time.Now()}
			api.db.Where("mcp_account_id = ? AND installation_id = ?", account.ID, installation.ID).Assign(conn).FirstOrCreate(&conn)
			rs, repositoriesErr := c.UserRepositories(r.Context(), token, installation.ID)
			if repositoriesErr != nil {
				continue
			}
			for _, repo := range rs {
				if seen[repo.ID] {
					continue
				}
				seen[repo.ID] = true
				repos = append(repos, map[string]any{"id": repo.ID, "full_name": repo.FullName, "clone_url": repo.CloneURL, "html_url": repo.HTMLURL, "default_branch": repo.DefaultBranch, "installation_id": installation.ID})
			}
		}
	}
	api.respondJSON(w, 200, repos)
}
func (api *API) GitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		api.respondError(w, 400, "invalid webhook body")
		return
	}
	forwarded := r.Header.Get(forwardedWebhookSignatureHeader) != ""
	if forwarded {
		if !validForwardedWebhook(body, r.Header.Get(forwardedWebhookSignatureHeader), os.Getenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET")) {
			api.respondError(w, 401, "invalid forwarded webhook signature")
			return
		}
	} else {
		c, err := githubClient()
		if err != nil || !c.VerifyWebhook(body, r.Header.Get("X-Hub-Signature-256")) {
			api.respondError(w, 401, "invalid GitHub webhook signature")
			return
		}
	}
	event := r.Header.Get("X-GitHub-Event")
	if !forwarded {
		go forwardGitHubWebhook(body, event)
	}
	var p struct {
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
		Issue struct {
			Number      int `json:"number"`
			PullRequest any `json:"pull_request"`
		} `json:"issue"`
		Comment struct {
			Body string `json:"body"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"comment"`
		WorkflowRun struct {
			Conclusion   string `json:"conclusion"`
			PullRequests []struct {
				Number int `json:"number"`
			} `json:"pull_requests"`
			HTMLURL string `json:"html_url"`
		} `json:"workflow_run"`
	}
	if json.Unmarshal(body, &p) != nil {
		api.respondError(w, 400, "invalid GitHub event")
		return
	}
	pr := p.PullRequest.Number
	if pr == 0 {
		pr = p.Issue.Number
	}
	if pr == 0 && len(p.WorkflowRun.PullRequests) > 0 {
		pr = p.WorkflowRun.PullRequests[0].Number
	}
	if pr == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var projects []db.Project
	api.db.Where("github_repository_id = ?", p.Repository.ID).Find(&projects)
	for _, project := range projects {
		var task db.Task
		if api.db.Where("project_id = ? AND github_pr_number = ?", project.ID, pr).Order("id desc").First(&task).Error != nil {
			continue
		}
		content := ""
		switch event {
		case "issue_comment":
			if p.Issue.PullRequest != nil {
				content = fmt.Sprintf("GitHub comment from @%s on PR #%d: %s", p.Comment.User.Login, pr, p.Comment.Body)
			}
		case "workflow_run":
			if p.WorkflowRun.Conclusion == "failure" {
				content = fmt.Sprintf("GitHub Actions failed for PR #%d: %s", pr, p.WorkflowRun.HTMLURL)
			}
		}
		if content == "" {
			continue
		}
		comment, _ := api.q.CreateComment(r.Context(), db.Comment{TaskID: task.ID, AuthorType: "github", CommentType: "github", Content: content})
		api.hub.BroadcastEvent("comment_created", comment)
		go api.engine.RerunTask(r.Context(), task.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}
func GitHubTokenForProject(ctx context.Context, database *db.Queries, project db.Project) (string, error) {
	if project.GitHubInstallationID == 0 {
		return "", nil
	}
	c, err := githubClient()
	if err != nil {
		return "", err
	}
	return c.InstallationToken(ctx, project.GitHubInstallationID, project.GitHubRepositoryID)
}
func GitHubRepoSelection(project *db.Project, raw string) {
	var in struct {
		ID             int64  `json:"id"`
		InstallationID int64  `json:"installation_id"`
		CloneURL       string `json:"clone_url"`
		DefaultBranch  string `json:"default_branch"`
	}
	if json.Unmarshal([]byte(raw), &in) == nil && in.ID > 0 {
		project.GitHubRepositoryID = in.ID
		project.GitHubInstallationID = in.InstallationID
		project.RepositoryUrl = in.CloneURL
		project.GitHubDefaultBranch = in.DefaultBranch
	}
}

var _ = chi.URLParam
