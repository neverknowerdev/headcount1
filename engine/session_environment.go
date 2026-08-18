package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
	gitpkg "agent-orchestrator/pkg/git"
	"agent-orchestrator/pkg/githubapp"
	"agent-orchestrator/pkg/logging"
)

type sessionEnvironment struct {
	company       db.Company
	rootTask      db.Task
	rootRunID     int32
	rootTaskID    int32
	groupMode     bool
	provider      db.LLMProvider
	model         string
	workspacePath string
	readOnlyDirs  []string
	artifactDir   string
	logger        *logging.ProxyLogger
	gitProject    bool
	gitManager    *gitpkg.GitManager
	cleanups      []func()
}

func (environment *sessionEnvironment) close() {
	for i := len(environment.cleanups) - 1; i >= 0; i-- {
		environment.cleanups[i]()
	}
}

func (e *NativeEngine) prepareSessionEnvironment(
	ctx context.Context,
	task *db.Task,
	agent db.Agent,
	run db.Run,
	parent *parentSession,
	resumed bool,
) (sessionEnvironment, db.Run, error) {
	var environment sessionEnvironment
	company, err := e.q.GetCompany(ctx, task.CompanyID)
	if err != nil {
		return environment, run, fmt.Errorf("failed to get company: %w", err)
	}
	environment.company = company
	environment.rootRunID = run.ID
	environment.rootTaskID = task.ID
	if parent != nil {
		environment.rootRunID = parent.rootRunID
		environment.rootTaskID = parent.rootTaskID
	}
	environment.provider, environment.model, err = resolveProvider(ctx, e.q, agent)
	if err != nil {
		return environment, run, err
	}
	// Internal-purpose runs use the same synthetic proxy provider as agents
	// bound to a model group. Detect it from the resolved target so helper
	// workers cannot accidentally call the first (usually free) member directly.
	environment.groupMode = agent.ModelGroupID != nil || isModelGroupProxyBaseURL(environment.provider.BaseUrl)
	if !resumed {
		run = assignRunName(ctx, e.q, *task, agent, run, parent, environment.rootTaskID, environment.rootRunID)
	}

	settings := loadSettings()
	manager := filesystem.NewManager(settings.BasePath)
	environment.rootTask, err = e.q.GetRootTask(ctx, task.ID)
	if err != nil {
		environment.rootTask = *task
	}
	if branchErr := e.q.EnsureTaskGitBranch(ctx, task); branchErr != nil {
		fmt.Printf("Warning: failed to ensure task Git branch: %v\n", branchErr)
	} else if task.ParentID != nil {
		if refreshed, refreshErr := e.q.GetRootTask(ctx, task.ID); refreshErr == nil {
			environment.rootTask = refreshed
		}
	}
	environment.workspacePath = manager.GetTaskWorktreePath(company, environment.rootTask)

	logger, logErr := logging.NewSessionLoggerWithHub(settings.BasePath, company.ShortName, environment.rootTaskID, environment.rootRunID, run.ID, e.hub.ForCompany(task.CompanyID), e.q)
	if logErr != nil {
		fmt.Printf("Warning: failed to create proxy logger: %v\n", logErr)
	} else {
		environment.logger = logger
		environment.cleanups = append(environment.cleanups, func() { _ = logger.Close() })
		_ = e.q.UpdateRunLogFilePath(ctx, run.ID, logger.FilePath())
	}

	if task.ProjectID != nil {
		project, projectErr := e.q.GetProject(ctx, *task.ProjectID)
		if projectErr == nil && project.RepositoryUrl != "" {
			environment.gitProject = true
			projectRepoDir := manager.GetProjectRepoPath(company, project)
			environment.readOnlyDirs = append(environment.readOnlyDirs, projectRepoDir)
			keyPath, keyCleanup := filesystem.ResolveSSHKeyPathForCompany(ctx, e.q, settings.BasePath, company)
			environment.cleanups = append(environment.cleanups, keyCleanup)
			environment.gitManager = gitpkg.NewGitManager(projectRepoDir, keyPath)
			if project.GitHubInstallationID != 0 {
				if token, tokenErr := githubapp.TokenForProject(ctx, project); tokenErr == nil && token != "" {
					environment.gitManager.WithHTTPToken(token)
				} else if tokenErr != nil {
					e.logInfo(environment.logger, "GitHub App token error: "+tokenErr.Error())
				}
			}
			if pullErr := environment.gitManager.Pull(ctx); pullErr != nil {
				e.logInfo(environment.logger, "Warning: git pull failed: "+pullErr.Error())
			}
			if _, statErr := os.Stat(filepath.Join(environment.workspacePath, ".git")); os.IsNotExist(statErr) {
				branchName := strings.TrimSpace(environment.rootTask.GitHubBranch)
				if branchName == "" {
					branchName = db.TaskGitBranch(environment.rootTask.RefKey, environment.rootTask.ID)
					environment.rootTask.GitHubBranch = branchName
					if _, updateErr := e.q.UpdateTask(ctx, environment.rootTask); updateErr != nil {
						e.logInfo(environment.logger, "Failed to persist task Git branch: "+updateErr.Error())
						environment.gitProject = false
					}
				}
				_ = os.RemoveAll(environment.workspacePath)
				if environment.gitProject {
					if worktreeErr := environment.gitManager.CreateWorktree(ctx, projectRepoDir, environment.workspacePath, branchName, "origin/"+environment.rootTask.EffectiveGitBaseBranch()); worktreeErr != nil {
						e.logInfo(environment.logger, "Failed to create worktree: "+worktreeErr.Error())
						environment.gitProject = false
					}
				}
			}
		}
	}
	if err := os.MkdirAll(environment.workspacePath, 0o755); err != nil {
		environment.close()
		return environment, run, fmt.Errorf("failed to create workspace: %w", err)
	}
	environment.artifactDir = manager.Paths().TaskArtifactsDir(company.ShortName, environment.rootTaskID)
	environment.readOnlyDirs = append(environment.readOnlyDirs, environment.artifactDir)
	return environment, run, nil
}
