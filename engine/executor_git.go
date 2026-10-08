package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/pkg/filesystem"
	gitpkg "agent-orchestrator/pkg/git"
	"agent-orchestrator/pkg/githubapp"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"
)

// commit commits what a writer changed in the shared worktree. It runs while
// the task still holds the worktree, so commits of one tree never interleave.
// A commit that cannot be made is logged and does not fail the task: the
// changes are in the worktree either way, and the next writer builds on them.
func (s *executorSession) commit(ctx context.Context) {
	if _, err := os.Stat(filepath.Join(s.workspace, ".git")); err != nil {
		s.info("Working directory is not a git worktree; nothing to commit")
		return
	}
	status, err := s.git.GetStatusInDir(ctx, s.workspace)
	if err != nil {
		s.info(fmt.Sprintf("Warning: could not read the worktree status: %v", err))
		return
	}
	if status = commitRelevantGitStatus(status); strings.TrimSpace(status) == "" {
		s.info("No changes to commit")
		return
	}
	diff, err := s.git.GetDiffInDir(ctx, s.workspace)
	if err != nil {
		s.info(fmt.Sprintf("Warning: could not read the diff: %v", err))
		return
	}
	// git diff leaves out untracked and staged-only changes; the status still
	// tells the message writer what changed.
	if strings.TrimSpace(diff) == "" {
		diff = status
	}
	message, err := s.commitMessage(ctx, diff)
	if err != nil {
		s.info(fmt.Sprintf("Warning: could not write a commit message (%v); using the task title", err))
		message = s.task.Title
	}
	if err := s.git.CommitInWorktree(ctx, s.workspace, message); err != nil {
		s.info(fmt.Sprintf("Warning: commit failed: %v", err))
		return
	}
	s.info("Committed changes: " + message)
}

// commitMessage asks a model to summarise a diff as a commit message. It uses
// the commit-messages model when one is configured, otherwise the session's.
func (s *executorSession) commitMessage(ctx context.Context, diff string) (string, error) {
	target := s.target
	if s.company.UserID != nil {
		if configured, err := resolveDefaultModel(ctx, s.e.q, *s.company.UserID, db.PurposeCommitMessages, models.TierCommit); err == nil {
			target = configured
		}
	}
	target.Tier = models.TierCommit
	const maxDiffChars = 60000
	if len(diff) > maxDiffChars {
		diff = diff[:maxDiffChars] + "\n... (truncated)"
	}
	prompt := fmt.Sprintf(`Summarize these code changes into a concise git commit message.
Subject line max 72 chars. Optional body separated by blank line.
Respond with ONLY the commit message, no quotes or explanation.

Task: %s
Changes:
%s`, s.task.Title, diff)

	apiKey, err := secrets.Default().Decrypt(target.Provider.ApiKeyEncrypted)
	if err != nil {
		return "", err
	}
	client := s.e.driver.newClient(target.Provider.BaseUrl, apiKey, target.Model)
	if target.viaGateway() {
		token, revoke := runtokens.Default().IssueCompany(s.task.CompanyID)
		defer revoke()
		client.ExtraHeaders = map[string]string{runtokens.TokenHeader: token}
	}
	usage := callContextFor(s.task, models.TaskPhaseExecute, "commit_message", &s.agent)
	runID := s.run.ID
	usage.RunID = &runID

	started := time.Now()
	response, _, err := client.Complete(ctx, aicli.ChatRequest{
		Messages:  []aicli.Message{{Role: "user", Content: prompt}},
		MaxTokens: 200,
	})
	if err != nil {
		recordCall(ctx, s.e.q, usage, target, "", aicli.Usage{}, time.Since(started), models.LLMCallError, err.Error())
		return "", err
	}
	recordCall(ctx, s.e.q, usage, target, response.Model, response.Usage, time.Since(started), models.LLMCallOK, "")
	if len(response.Choices) == 0 {
		return "", errors.New("the model returned no message")
	}
	message := strings.Trim(strings.TrimSpace(response.Choices[0].Message.Content), "\"'`")
	if message == "" {
		return "", errors.New("the model returned an empty message")
	}
	return message, nil
}

// publishPR pushes a top-level coding task's branch and opens its pull
// request once verification has passed. What happened is written to the
// task's journal either way; a pull request that cannot be opened does not
// undo the verdict.
func (e *NativeEngine) publishPR(task db.Task) {
	ctx := context.Background()
	note := func(format string, args ...interface{}) {
		step, err := e.q.AppendTaskStep(ctx, db.TaskStep{
			TaskID: task.ID, RootTaskID: task.RootTaskID, Kind: models.StepNote,
			Result: fmt.Sprintf(format, args...), CreatedAt: time.Now(),
		})
		if err == nil {
			e.hub.BroadcastEventForCompany(task.CompanyID, "task_step", map[string]interface{}{"task_id": task.ID, "step": step})
		}
	}
	if task.ProjectID == nil {
		return
	}
	project, err := e.q.GetProject(ctx, *task.ProjectID)
	if err != nil || project.RepositoryUrl == "" || project.GitHubInstallationID == 0 {
		return
	}
	company, err := e.q.GetCompany(ctx, task.CompanyID)
	if err != nil {
		return
	}
	settings := loadSettings()
	manager := filesystem.NewManager(settings.BasePath)
	worktree := manager.GetTaskWorktreePath(company, task)
	keyPath, keyCleanup := filesystem.ResolveSSHKeyPathForCompany(ctx, e.q, settings.BasePath, company)
	defer keyCleanup()
	token, err := githubapp.TokenForProject(ctx, project)
	if err != nil || token == "" {
		note("Pull request not opened: GitHub installation token failed (%v)", err)
		return
	}
	git := gitpkg.NewGitManager(manager.GetProjectRepoPath(company, project), keyPath).WithHTTPToken(token)

	branch := strings.TrimSpace(task.GitHubBranch)
	if branch == "" {
		branch = db.TaskGitBranch(task.RefKey, task.ID)
	}
	changed, err := git.HasChangesFromBase(ctx, worktree, task.EffectiveGitBaseBranch())
	if err != nil {
		note("Pull request not opened: could not compare with the base branch (%v)", err)
		return
	}
	if !changed {
		note("No committed changes on %s; no pull request needed", branch)
		return
	}
	if err := git.PushWorktreeBranch(ctx, worktree, branch); err != nil {
		note("Pull request not opened: push failed (%v)", err)
		return
	}
	if task.GitHubPRNumber != 0 {
		note("Pushed to the existing pull request #%d: %s", task.GitHubPRNumber, task.GitHubPRURL)
		return
	}
	client, err := githubapp.FromEnv()
	if err != nil {
		note("Pull request not opened: GitHub App is not configured (%v)", err)
		return
	}
	slug, err := githubapp.RepositorySlug(project.RepositoryUrl)
	if err != nil {
		note("Pull request not opened: %v", err)
		return
	}
	record := func(number int, url string) {
		updated, err := e.q.UpdateTaskFields(ctx, task.ID, map[string]interface{}{
			"git_hub_branch": branch, "git_hub_pr_number": number, "git_hub_pr_url": url,
		})
		if err == nil {
			e.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", updated)
		}
	}
	owner := strings.SplitN(slug, "/", 2)[0]
	if existing, found, err := client.FindOpenPullRequestByHead(ctx, token, slug, owner+":"+branch); err == nil && found {
		record(existing.Number, existing.HTMLURL)
		note("Pushed to the existing pull request #%d: %s", existing.Number, existing.HTMLURL)
		return
	}
	description := strings.TrimSpace(task.ResultSummary)
	if spec := strings.TrimSpace(task.RefinedDescription); spec != "" {
		description += "\n\n## Specification\n\n" + spec
	}
	number, url, err := client.CreatePullRequest(ctx, token, slug, task.Title, branch, task.EffectiveGitBaseBranch(), description)
	if err != nil {
		note("Pull request not opened: %v", err)
		return
	}
	record(number, url)
	note("Opened pull request #%d: %s", number, url)
}
