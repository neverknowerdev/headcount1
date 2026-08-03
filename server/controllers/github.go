package endpoints

import (
	"context"
	"crypto/rand"
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
	"github.com/go-chi/chi/v5"
)

func githubClient() (*githubapp.Client, error) { return githubapp.FromEnv() }
func deploymentURL() string                    { return strings.TrimRight(os.Getenv("DEPLOY_URL"), "/") }

func (api *API) GitHubStatus(w http.ResponseWriter, r *http.Request) {
	c, err := githubClient()
	if err != nil {
		api.respondJSON(w, http.StatusOK, map[string]any{"configured": false, "error": err.Error()})
		return
	}
	var connections []db.GitHubConnection
	api.db.Order("id").Find(&connections)
	api.respondJSON(w, http.StatusOK, map[string]any{"configured": true, "install_url": c.InstallURL(), "connections": connections})
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
	token, err := c.ExchangeCode(r.Context(), code)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	installs, err := c.UserInstallations(r.Context(), token)
	if err != nil {
		http.Error(w, err.Error(), 400)
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
	var conns []db.GitHubConnection
	api.db.Find(&conns)
	repos := []map[string]any{}
	for _, conn := range conns {
		if conn.UserAccessToken == "" {
			continue
		}
		rs, e := c.UserRepositories(r.Context(), conn.UserAccessToken, conn.InstallationID)
		if e != nil {
			continue
		}
		for _, repo := range rs {
			repos = append(repos, map[string]any{"id": repo.ID, "full_name": repo.FullName, "clone_url": repo.CloneURL, "html_url": repo.HTMLURL, "default_branch": repo.DefaultBranch, "installation_id": conn.InstallationID})
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
	c, err := githubClient()
	if err != nil || !c.VerifyWebhook(body, r.Header.Get("X-Hub-Signature-256")) {
		api.respondError(w, 401, "invalid GitHub webhook signature")
		return
	}
	event := r.Header.Get("X-GitHub-Event")
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
