package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/git"
	"agent-orchestrator/pkg/logging"
	"gorm.io/gorm"
)

// NativeEngine implements Engine using the aicli package for direct LLM communication.
type NativeEngine struct {
	q           *db.Queries
	hub         *eventhub.Hub
	cancelFuncs sync.Map // runID -> context.CancelFunc
}

// NewNativeEngine creates a NativeEngine. No external process setup is required.
func NewNativeEngine(database *gorm.DB, hub *eventhub.Hub) *NativeEngine {
	return &NativeEngine{
		q:   db.New(database),
		hub: hub,
	}
}

// ProcessTask picks up a task and spawns a goroutine to run the agent.
func (e *NativeEngine) ProcessTask(ctx context.Context, taskID int32) error {
	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	// Deduplication: skip if a non-stale run is already active.
	if task.RunID != nil {
		isStale, err := e.q.IsRunStale(ctx, *task.RunID, 5*time.Minute)
		if err != nil {
			return fmt.Errorf("failed to check run staleness: %w", err)
		}
		if !isStale {
			return nil
		}
		fmt.Printf("Task %d has stale run %d, resolving before new run\n", taskID, *task.RunID)
		e.resolveStaleRun(ctx, *task.RunID)
	}

	switch task.Status {
	case "to-do":
		if task.TaskType == db.TaskTypeImplement {
			task.Status = "in-progress"
			if _, err := e.q.UpdateTask(ctx, task); err != nil {
				return err
			}
			e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": "in-progress"})
			go e.run(context.Background(), task, "implement")
		} else {
			task.Status = "refinement"
			if _, err := e.q.UpdateTask(ctx, task); err != nil {
				return err
			}
			e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": "refinement"})
			go e.run(context.Background(), task, "plan")
		}
	case "in-progress":
		go e.run(context.Background(), task, "implement")
	}

	return nil
}

// StopRun cancels the context for the given run, interrupting it at the next
// context check inside the agent loop.
func (e *NativeEngine) StopRun(ctx context.Context, runID int32) {
	if val, loaded := e.cancelFuncs.LoadAndDelete(runID); loaded {
		if cancel, ok := val.(context.CancelFunc); ok {
			cancel()
		}
	}
}

// resolveStaleRun marks a stale run as failed and unlocks its task.
func (e *NativeEngine) resolveStaleRun(ctx context.Context, runID int32) {
	run, err := e.q.GetRun(ctx, runID)
	if err != nil {
		return
	}
	e.q.UpdateRunLog(ctx, runID, "Run marked as failed: previous run no longer active", "failed")
	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": runID, "status": "failed"})
	e.q.UnlockTaskRun(ctx, run.TaskID)
}

// run is the goroutine body for a single agent execution.
func (e *NativeEngine) run(ctx context.Context, task db.Task, mode string) {
	if task.AgentID == nil {
		return
	}

	agent, err := e.q.GetAgent(ctx, *task.AgentID)
	if err != nil {
		return
	}

	run, err := e.q.CreateRun(ctx, db.Run{
		TaskID:    task.ID,
		AgentID:   agent.ID,
		Status:    "running",
		StartedAt: time.Now(),
	})
	if err != nil {
		return
	}

	// Register the cancel func and lock the task immediately so that
	// StopRun and test pollers (waitForRunCreated) can observe the run right
	// away — before any potentially slow filesystem or DB work.
	runCtx, cancel := context.WithCancel(context.Background())
	e.cancelFuncs.Store(run.ID, cancel)
	defer func() {
		cancel()
		e.cancelFuncs.Delete(run.ID)
	}()

	if lockErr := e.q.LockTaskRun(ctx, task.ID, run.ID); lockErr != nil {
		fmt.Printf("Warning: failed to lock task %d for run %d: %v\n", task.ID, run.ID, lockErr)
	}
	defer func() {
		if clearErr := e.q.UnlockTaskRun(context.Background(), task.ID); clearErr != nil {
			fmt.Printf("Warning: failed to unlock task %d: %v\n", task.ID, clearErr)
		}
	}()

	e.hub.BroadcastEvent("run_started", run)

	// Write initial run metadata to filesystem.
	settings := loadSettings()
	storage := filesystem.NewStorage(settings.BasePath)
	companyShortName, _ := storage.GetCompanyShortNameForTask(task.ID)
	if companyShortName != "" {
		if err := storage.WriteRun(run, companyShortName); err != nil {
			fmt.Printf("Warning: failed to write run metadata: %v\n", err)
		}
	}

	// Resolve provider from the agent configuration.
	if agent.ProviderID == nil {
		e.failRun(ctx, run.ID, "agent has no provider configured")
		return
	}
	provider, err := e.q.GetLLMProvider(ctx, *agent.ProviderID)
	if err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("failed to get provider: %v", err))
		return
	}

	model := agent.Model
	if model == "" {
		model = provider.DefaultModel
	}

	company, compErr := e.q.GetCompany(ctx, task.CompanyID)
	if compErr != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("failed to get company: %v", compErr))
		return
	}

	fsMgr := filesystem.NewManager(settings.BasePath)
	workspacePath := fsMgr.GetTaskWorktreePath(company, task)
	if err := os.MkdirAll(workspacePath, 0755); err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("failed to create workspace: %v", err))
		return
	}

	if err := initTaskMemory(workspacePath, task, company); err != nil {
		fmt.Printf("Warning: failed to init memory.md: %v\n", err)
	}

	// Set up proxy logger (same format as the LLM gateway so UI and file layout match).
	proxyLogger, logErr := logging.NewProxyLoggerWithHub(
		settings.BasePath,
		company.ShortName,
		task.ID,
		run.ID,
		e.hub,
		e.q,
	)
	if logErr != nil {
		fmt.Printf("Warning: failed to create proxy logger: %v\n", logErr)
	} else {
		defer proxyLogger.Close()
		e.q.UpdateRunLogFilePath(ctx, run.ID, proxyLogger.FilePath())
	}

	// Git worktree setup.
	var gitProject bool
	var gitMgr *git.GitManager
	if task.ProjectID != nil {
		project, projErr := e.q.GetProject(ctx, *task.ProjectID)
		if projErr == nil && project.RepositoryUrl != "" {
			gitProject = true
			projectRepoDir := fsMgr.GetProjectRepoPath(company, project)
			sshDir := filepath.Join(settings.BasePath, ".ssh")
			gitMgr = git.NewGitManager(projectRepoDir, sshDir)
			if pullErr := gitMgr.Pull(ctx); pullErr != nil {
				e.logInfo(proxyLogger, "Warning: git pull failed: "+pullErr.Error())
			}
			if _, statErr := os.Stat(workspacePath); os.IsNotExist(statErr) {
				branchName := fmt.Sprintf("task-%d", task.ID)
				if wtErr := gitMgr.CreateWorktree(ctx, projectRepoDir, workspacePath, branchName, "origin/main"); wtErr != nil {
					e.logInfo(proxyLogger, "Failed to create worktree: "+wtErr.Error())
					gitProject = false
				}
			}
		}
	}

	// Build system prompt and user context.
	promptBuilder := NewSystemPromptBuilder(e.q)
	systemPrompt := promptBuilder.Build(agent, task)

	comments, _ := e.q.ListCommentsByTask(ctx, task.ID)
	attachments, _ := e.q.ListAttachmentsByTask(ctx, task.ID)

	var contextParts []string
	contextParts = append(contextParts, fmt.Sprintf("Task: %s\nDescription: %s\nMode: %s\n", task.Title, task.Description, mode))

	if len(attachments) > 0 {
		contextParts = append(contextParts, "Attachments:")
		for _, a := range attachments {
			if strings.HasPrefix(a.MimeType, "image/") {
				contextParts = append(contextParts, fmt.Sprintf("- %s (image)", a.Filename))
			} else {
				contextParts = append(contextParts, fmt.Sprintf("- %s", a.Filename))
			}
		}
	}

	if len(comments) > 0 {
		contextParts = append(contextParts, "Comments:")
		for _, c := range comments {
			contextParts = append(contextParts, fmt.Sprintf("[%s]: %s", c.AuthorType, c.Content))
		}
	}

	userMessage := strings.Join(contextParts, "\n")

	// Build tool registry: default file/shell/web tools + update_task_status.
	registry := aicli.DefaultTools(workspacePath)
	registry.Register(aicli.UpdateTaskStatusTool(func(updateCtx context.Context, status string) error {
		t, err := e.q.GetTask(updateCtx, task.ID)
		if err != nil {
			return err
		}
		t.Status = status
		if _, err := e.q.UpdateTask(updateCtx, t); err != nil {
			return err
		}
		e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": status})
		return nil
	}))

	// Wire the proxy logger as the agent's RunLogger so request/response entries
	// appear in the log file and the DB (identical format to the gateway).
	llmClient := aicli.NewClient(provider.BaseUrl, provider.ApiKey, model)
	agentCfg := aicli.Config{
		Client:       llmClient,
		Registry:     registry,
		Mode:         aicli.ModeMessageHistory,
		ProviderName: provider.Name,
		AgentName:    agent.Name,
		Queries:      e.q,
		RunID:        run.ID,
		Logger:       proxyLogger,
	}
	aiAgent := aicli.New(agentCfg)

	e.logInfo(proxyLogger, fmt.Sprintf("Starting native agent for task %d (mode=%s model=%s provider=%s)", task.ID, mode, model, provider.Name))
	e.logInfo(proxyLogger, fmt.Sprintf("Workspace: %s", workspacePath))

	finalText, agentErr := aiAgent.Run(runCtx, systemPrompt, userMessage)

	status := "completed"

	if agentErr != nil {
		if runCtx.Err() == context.Canceled {
			e.logInfo(proxyLogger, "Run canceled by user")
			e.q.UpdateRunLog(ctx, run.ID, "", "canceled")
			e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": "canceled"})
			return
		}
		status = "failed"
		e.logError(proxyLogger, fmt.Sprintf("Agent error: %v", agentErr))
	}

	// If the agent returned text, save it as a comment.
	if finalText != "" {
		comment, _ := e.q.CreateComment(ctx, db.Comment{
			TaskID:     task.ID,
			AuthorType: "agent",
			Content:    finalText,
		})
		e.hub.BroadcastEvent("comment_created", comment)
	}

	// If the task status was not updated during the run, force a follow-up turn.
	taskAfter, _ := e.q.GetTask(ctx, task.ID)
	if agentErr == nil && taskAfter.Status == task.Status {
		e.logInfo(proxyLogger, "Task status not updated by agent. Sending follow-up to force update.")
		followUpText, followErr := aiAgent.Run(runCtx, systemPrompt,
			"Please call update_task_status with the appropriate status: in-progress, blocked, or in-review.")
		if followErr == nil && followUpText != "" {
			comment, _ := e.q.CreateComment(ctx, db.Comment{
				TaskID:     task.ID,
				AuthorType: "agent",
				Content:    followUpText,
			})
			e.hub.BroadcastEvent("comment_created", comment)
		}
	}

	// Git commit if there are changes.
	if gitProject && gitMgr != nil && status == "completed" {
		e.tryGitCommit(ctx, proxyLogger, gitMgr, workspacePath, task, agent)
	}

	// Emit final token summary.
	if finalStats, err := e.q.GetRunTokenStats(ctx, run.ID); err == nil {
		e.logInfo(proxyLogger, fmt.Sprintf(
			"=== Token Totals === prompt=%d completion=%d reasoning=%d tool_in=%d tool_out=%d total=%d",
			finalStats.PromptTokens, finalStats.CompletionTokens,
			finalStats.ReasoningTokens, finalStats.ToolInputTokens,
			finalStats.ToolOutputTokens, finalStats.TotalTokens,
		))
	}

	e.q.UpdateRunLog(ctx, run.ID, "", status)

	// Update run metadata in filesystem.
	settings = loadSettings()
	storage = filesystem.NewStorage(settings.BasePath)
	if csn, _ := storage.GetCompanyShortNameForTask(task.ID); csn != "" {
		if updatedRun, err := e.q.GetRun(ctx, run.ID); err == nil {
			storage.WriteRun(updatedRun, csn)
		}
	}

	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": status})
}

// failRun marks a run as failed and broadcasts the event.
func (e *NativeEngine) failRun(ctx context.Context, runID int32, errMsg string) {
	e.q.UpdateRunLog(ctx, runID, errMsg, "failed")
	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": runID, "status": "failed"})
}

// logInfo writes an info entry to the proxy logger (if non-nil).
func (e *NativeEngine) logInfo(logger *logging.ProxyLogger, msg string) {
	if logger == nil {
		fmt.Println(msg)
		return
	}
	logger.LogInfo(msg)
}

// logError writes an error entry to the proxy logger (if non-nil).
func (e *NativeEngine) logError(logger *logging.ProxyLogger, msg string) {
	if logger == nil {
		fmt.Println("ERROR:", msg)
		return
	}
	logger.LogErrorMsg(msg)
}

// tryGitCommit generates a commit message and commits workspace changes.
func (e *NativeEngine) tryGitCommit(ctx context.Context, logger *logging.ProxyLogger, gitMgr *git.GitManager, workspacePath string, task db.Task, agent db.Agent) {
	e.logInfo(logger, "Checking for changes to commit in worktree...")
	diff, err := gitMgr.GetDiffInDir(ctx, workspacePath)
	if err != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to get diff: %v", err))
		return
	}
	if strings.TrimSpace(diff) == "" {
		e.logInfo(logger, "No changes to commit")
		return
	}

	// Generate commit message via LLM.
	commitMsg, msgErr := e.generateCommitMessage(ctx, agent, diff, task)
	if msgErr != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to generate commit message: %v, using fallback", msgErr))
		commitMsg = fmt.Sprintf("Agent run for task %d", task.ID)
	}

	if commitErr := gitMgr.CommitInWorktree(ctx, workspacePath, commitMsg); commitErr != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to commit: %v", commitErr))
	} else {
		e.logInfo(logger, fmt.Sprintf("Committed changes: %s", commitMsg))
	}
}

// generateCommitMessage calls the LLM to summarise a diff into a commit message.
func (e *NativeEngine) generateCommitMessage(ctx context.Context, agent db.Agent, diff string, task db.Task) (string, error) {
	if agent.ProviderID == nil {
		return "", fmt.Errorf("no provider configured")
	}
	provider, err := e.q.GetLLMProvider(ctx, *agent.ProviderID)
	if err != nil {
		return "", err
	}
	model := agent.Model
	if model == "" {
		model = provider.DefaultModel
	}
	if len(diff) > 8000 {
		diff = diff[:8000] + "\n... (truncated)"
	}
	prompt := fmt.Sprintf(`Summarize these code changes into a concise git commit message.
Subject line max 72 chars. Optional body separated by blank line.
Respond with ONLY the commit message, no quotes or explanation.

Task: %s
Changes:
%s`, task.Title, diff)

	client := aicli.NewClient(provider.BaseUrl, provider.ApiKey, model)
	resp, _, err := client.Complete(ctx, aicli.ChatRequest{
		Messages:  []aicli.Message{{Role: "user", Content: prompt}},
		MaxTokens: 200,
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	msg := strings.TrimSpace(resp.Choices[0].Message.Content)
	msg = strings.Trim(msg, "\"'`")
	if msg == "" {
		return "", fmt.Errorf("empty commit message")
	}

	return msg, nil
}
