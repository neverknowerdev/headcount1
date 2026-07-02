package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/git"
	"agent-orchestrator/pkg/logging"
	"gorm.io/gorm"
)

// NativeEngine implements Engine using the aicli package for direct LLM communication.
type NativeEngine struct {
	q            *db.Queries
	hub          *eventhub.Hub
	agentFactory agentconfig.Factory
	cancelFuncs  sync.Map // runID -> context.CancelFunc
}

// NewNativeEngine creates a NativeEngine pre-loaded with the default agent
// config factory.
func NewNativeEngine(database *gorm.DB, hub *eventhub.Hub) *NativeEngine {
	return &NativeEngine{
		q:            db.New(database),
		hub:          hub,
		agentFactory: agentconfig.NewDefaultFactory(),
	}
}

// defaultOrchestratorConfig is the agent config every root task is routed
// through: the CEO orchestrates execution via delegation to specialists.
const defaultOrchestratorConfig = "CEO"

// maxDelegationDepth caps how deep delegation sessions can nest
// (root CEO session is depth 0).
const maxDelegationDepth = 3

// parentSession links a delegated child session to the session that spawned
// it: run hierarchy ids, the shared workspace, and a hook that fires once the
// child's run record exists (so the parent can log the session start).
type parentSession struct {
	parentRunID   int32
	rootRunID     int32
	rootTaskID    int32
	workspacePath string
	depth         int
	onRunCreated  func(run db.Run)
}

// ProcessTask picks up a task and spawns a goroutine to run the agent.
func (e *NativeEngine) ProcessTask(ctx context.Context, taskID int32) error {
	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	// Route every root task through the CEO orchestrator unless the task
	// already pins a specific agent config.
	if task.ParentID == nil && task.AgentConfigName == "" && e.agentFactory != nil {
		if _, cfgErr := e.agentFactory.GetConfig(defaultOrchestratorConfig); cfgErr == nil {
			task.AgentConfigName = defaultOrchestratorConfig
			if updated, upErr := e.q.UpdateTask(ctx, task); upErr == nil {
				task = updated
			}
		}
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
			prevStatus := task.Status
			task.Status = "in-progress"
			if _, err := e.q.UpdateTask(ctx, task); err != nil {
				return err
			}
			e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": "in-progress"})
			e.emitStatusChange(ctx, task.ID, prevStatus, "in-progress")
			go e.run(context.Background(), task, "implement")
		} else {
			prevStatus := task.Status
			task.Status = "refinement"
			if _, err := e.q.UpdateTask(ctx, task); err != nil {
				return err
			}
			e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": "refinement"})
			e.emitStatusChange(ctx, task.ID, prevStatus, "refinement")
			go e.run(context.Background(), task, "plan")
		}
	case "in-progress":
		go e.run(context.Background(), task, "implement")
	case "in-review", "blocked", "done", "refinement":
		// Re-run triggered by a user comment (Run Agent flag). Move back to in-progress.
		prevStatus := task.Status
		task.Status = "in-progress"
		if _, err := e.q.UpdateTask(ctx, task); err != nil {
			return err
		}
		e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": "in-progress"})
		e.emitStatusChange(ctx, task.ID, prevStatus, "in-progress")
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

// run is the goroutine body for a single root agent execution.
func (e *NativeEngine) run(ctx context.Context, task db.Task, mode string) {
	e.executeSession(ctx, task, mode, nil)
}

// executeSession runs one agent session for a task and returns its final run
// status ("completed", "failed" or "canceled"). Root sessions (parent == nil)
// run detached from the caller's context; delegated child sessions inherit the
// parent's run context so stopping the parent stops the whole tree.
func (e *NativeEngine) executeSession(ctx context.Context, task db.Task, mode string, parent *parentSession) string {
	if task.AgentID == nil {
		return "failed"
	}

	agent, err := e.q.GetAgent(ctx, *task.AgentID)
	if err != nil {
		return "failed"
	}

	newRun := db.Run{
		TaskID:          task.ID,
		AgentID:         agent.ID,
		Status:          "running",
		StartedAt:       time.Now(),
		AgentConfigName: task.AgentConfigName,
	}
	if parent != nil {
		parentID := parent.parentRunID
		rootID := parent.rootRunID
		newRun.ParentRunID = &parentID
		newRun.RootRunID = &rootID
	}
	run, err := e.q.CreateRun(ctx, newRun)
	if err != nil {
		return "failed"
	}
	if parent == nil {
		// Root runs point at themselves so the whole tree shares one root id.
		rootID := run.ID
		run.RootRunID = &rootID
		if rootErr := e.q.SetRunRootID(ctx, run.ID, rootID); rootErr != nil {
			fmt.Printf("Warning: failed to set root run id for run %d: %v\n", run.ID, rootErr)
		}
	}

	// Register the cancel func and lock the task immediately so that
	// StopRun and test pollers (waitForRunCreated) can observe the run right
	// away — before any potentially slow filesystem or DB work.
	// Child sessions derive from the parent run's context so a parent stop
	// cancels them too.
	baseCtx := context.Background()
	if parent != nil {
		baseCtx = ctx
	}
	runCtx, cancel := context.WithCancel(baseCtx)
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

	// Let the delegating session record the child run before any slow work.
	if parent != nil && parent.onRunCreated != nil {
		parent.onRunCreated(run)
	}

	e.hub.BroadcastEvent("run_started", run)

	company, compErr := e.q.GetCompany(ctx, task.CompanyID)
	if compErr != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("failed to get company: %v", compErr))
		return "failed"
	}

	// Write initial run metadata to filesystem.
	settings := loadSettings()
	storage := filesystem.NewStorage(settings.BasePath)
	if err := storage.WriteRun(run, company.ShortName); err != nil {
		fmt.Printf("Warning: failed to write run metadata: %v\n", err)
	}

	// Session hierarchy: which run/task the log folder is grouped under.
	rootRunID := run.ID
	rootTaskID := task.ID
	depth := 0
	if parent != nil {
		rootRunID = parent.rootRunID
		rootTaskID = parent.rootTaskID
		depth = parent.depth
	}

	// Resolve provider from the agent configuration.
	if agent.ProviderID == nil {
		e.failRun(ctx, run.ID, "agent has no provider configured")
		return "failed"
	}
	provider, err := e.q.GetLLMProvider(ctx, *agent.ProviderID)
	if err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("failed to get provider: %v", err))
		return "failed"
	}

	// Load agent config early so model resolution can use AllowedModels.
	// The proxy logger isn't ready yet, so fall back to stdout for this warning.
	var agentCfg *agentconfig.AgentConfig
	if task.AgentConfigName != "" && e.agentFactory != nil {
		if cfg, cfgErr := e.agentFactory.GetConfig(task.AgentConfigName); cfgErr == nil {
			agentCfg = cfg
		} else {
			fmt.Printf("Warning: agent config %q not found for task %d: %v\n", task.AgentConfigName, task.ID, cfgErr)
		}
	}

	// Resolve the model: intersect AgentConfig.AllowedModels with the
	// provider's SupportedModels, falling back to the agent/provider default.
	model, err := resolveModel(agentCfg, provider, agent.Model)
	if err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("model resolution failed: %v", err))
		return "failed"
	}

	// Delegated sessions share the root session's workspace so specialists
	// (e.g. Programmer then QA) see each other's changes.
	fsMgr := filesystem.NewManager(settings.BasePath)
	var workspacePath string
	if parent != nil && parent.workspacePath != "" {
		workspacePath = parent.workspacePath
	} else {
		workspacePath = fsMgr.GetTaskWorktreePath(company, task)
		if err := os.MkdirAll(workspacePath, 0755); err != nil {
			e.failRun(ctx, run.ID, fmt.Sprintf("failed to create workspace: %v", err))
			return "failed"
		}
		if err := initTaskMemory(workspacePath, task, company); err != nil {
			fmt.Printf("Warning: failed to init memory.md: %v\n", err)
		}
	}

	// Set up the session logger. All sessions of one main run share the
	// data/{company}/logs/{rootTaskID}/run-{rootRunID}/ folder: the root
	// session writes main.log, child sessions write session-{runID}.log.
	proxyLogger, logErr := logging.NewSessionLoggerWithHub(
		settings.BasePath,
		company.ShortName,
		rootTaskID,
		rootRunID,
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

	// Git worktree setup (root session only — children reuse its worktree).
	var gitProject bool
	var gitMgr *git.GitManager
	if parent == nil && task.ProjectID != nil {
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

	// Build system prompt.
	var systemPrompt string
	if agentCfg != nil && agentCfg.Prompt != "" {
		// Use config prompt as the base; append task context from the builder.
		taskContext := NewSystemPromptBuilder(e.q).Build(agent, task)
		systemPrompt = agentCfg.Prompt + "\n\n" + taskContext
	} else {
		systemPrompt = NewSystemPromptBuilder(e.q).Build(agent, task)
	}

	comments, _ := e.q.ListCommentsByTask(ctx, task.ID)
	attachments, _ := e.q.ListAttachmentsByTask(ctx, task.ID)
	pastRuns, _ := e.q.ListCompletedRunsByTask(ctx, task.ID)
	// Build initial messages: task description as the first user message,
	// then past run results and human/agent comments interleaved chronologically.
	taskContent := fmt.Sprintf("Task: %s\nDescription: %s\nMode: %s", task.Title, task.Description, mode)
	if len(attachments) > 0 {
		taskContent += "\nAttachments:"
		for _, a := range attachments {
			if strings.HasPrefix(a.MimeType, "image/") {
				taskContent += fmt.Sprintf("\n- %s (image)", a.Filename)
			} else {
				taskContent += fmt.Sprintf("\n- %s", a.Filename)
			}
		}
	}

	type timelineEntry struct {
		t    time.Time
		role string
		text string
	}
	var timeline []timelineEntry

	// Add past completed runs as compact JSON agent messages (description only, not explanation).
	for _, r := range pastRuns {
		ts := r.StartedAt
		if r.EndedAt != nil {
			ts = *r.EndedAt
		}
		msg := fmt.Sprintf(`{"run_id":%d,"completed_at":"%s","result":%q}`,
			r.ID, ts.Format(time.RFC3339), r.ResultDescription)
		timeline = append(timeline, timelineEntry{t: ts, role: "assistant", text: msg})
	}

	// Add human comments and non-task_done agent comments; format status changes readably.
	for _, c := range comments {
		if c.AuthorType == "agent" && c.CommentType == "task_done" {
			continue // already represented by the run result JSON above
		}
		role := "user"
		if c.AuthorType == "agent" || c.AuthorType == "system" {
			role = "assistant"
		}
		text := c.Content
		switch c.CommentType {
		case "status_change":
			var meta struct {
				From string `json:"from"`
				To   string `json:"to"`
			}
			if jsonErr := json.Unmarshal([]byte(c.Content), &meta); jsonErr == nil {
				actor := "User"
				if c.AuthorType != "human" {
					actor = "System"
				}
				text = fmt.Sprintf("[%s changed task status: %s → %s]", actor, meta.From, meta.To)
			}
		case "artifact_created":
			var meta struct {
				Filename string `json:"filename"`
			}
			if jsonErr := json.Unmarshal([]byte(c.Content), &meta); jsonErr == nil {
				text = fmt.Sprintf(`[Artifact created: "%s"]`, meta.Filename)
			}
		}
		timeline = append(timeline, timelineEntry{t: c.CreatedAt, role: role, text: text})
	}


	// Sort chronologically.
	for i := 1; i < len(timeline); i++ {
		for j := i; j > 0 && timeline[j].t.Before(timeline[j-1].t); j-- {
			timeline[j], timeline[j-1] = timeline[j-1], timeline[j]
		}
	}

	initialMessages := []aicli.Message{{Role: "user", Content: taskContent}}
	for _, entry := range timeline {
		initialMessages = append(initialMessages, aicli.Message{Role: entry.role, Content: entry.text})
	}

	// Build full tool registry: file/shell/web tools + task-management tools.
	registry := tools.DefaultRegistry(workspacePath)

	// Track whether finish_task was called so we can force it if not.
	var taskFinished bool

	registry.Register(tools.NewFinishTask(func(finCtx context.Context, status, finishStatus string) error {
		taskFinished = true
		t, err := e.q.GetTask(finCtx, task.ID)
		if err != nil {
			return err
		}
		prevStatus := t.Status
		t.Status = status
		if _, err := e.q.UpdateTask(finCtx, t); err != nil {
			return err
		}
		e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": status})
		if err := e.q.UpdateRunResult(finCtx, run.ID, finishStatus, ""); err != nil {
			fmt.Printf("Warning: failed to store run result: %v\n", err)
		}
		runID := run.ID
		content, _ := json.Marshal(map[string]string{"msg": finishStatus, "from": prevStatus, "to": status})
		comment, cErr := e.q.CreateComment(finCtx, db.Comment{
			TaskID:      task.ID,
			AuthorType:  "agent",
			CommentType: "task_done",
			Content:     string(content),
			RunID:       &runID,
		})
		if cErr == nil {
			e.hub.BroadcastEvent("comment_created", comment)
		}
		return nil
	}))

	// Resolve artifact directory: {basePath}/artifacts/{project_folder} or /artifacts/task-{id}
	artifactDir := func() string {
		base := settings.BasePath
		if task.ProjectID != nil && task.Project != nil && task.Project.WorkspaceFolder != "" {
			return filepath.Join(base, "artifacts", task.Project.WorkspaceFolder)
		}
		return filepath.Join(base, "artifacts", fmt.Sprintf("task-%d", task.ID))
	}()

	registry.Register(tools.NewWriteArtifactFile(func(wCtx context.Context, filename, content string) error {
		if err := os.MkdirAll(artifactDir, 0755); err != nil {
			return fmt.Errorf("could not create artifact directory: %w", err)
		}
		filePath := filepath.Join(artifactDir, filename)
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			return fmt.Errorf("could not write artifact file: %w", err)
		}
		artifact, err := e.q.CreateArtifact(wCtx, db.Artifact{
			TaskID:   task.ID,
			RunID:    run.ID,
			Filename: filename,
			FilePath: filePath,
			Content:  content,
		})
		if err != nil {
			fmt.Printf("Warning: failed to save artifact to DB: %v\n", err)
			return nil
		}
		e.hub.BroadcastEvent("artifact_created", artifact)
		commentContent, _ := json.Marshal(map[string]string{
			"artifact_id": fmt.Sprintf("%d", artifact.ID),
			"filename":    filename,
			"content":     content,
		})
		if ac, cErr := e.q.CreateComment(wCtx, db.Comment{
			TaskID:      task.ID,
			AuthorType:  "system",
			CommentType: "artifact_created",
			Content:     string(commentContent),
		}); cErr == nil {
			e.hub.BroadcastEvent("comment_created", ac)
		}
		return nil
	}))
	var agentNames []string
	if e.agentFactory != nil {
		agentNames = e.agentFactory.ListNames()
	}

	// Delegation: available until the nesting cap so a runaway chain of
	// agents can't recurse forever.
	if depth < maxDelegationDepth {
		registry.Register(tools.NewDelegateTask(
			e.makeDelegateFunc(task, agent, run, proxyLogger, rootRunID, rootTaskID, workspacePath, depth),
			agentNames,
		))
	}

	registry.Register(tools.NewAskHuman(func(qCtx context.Context, question string) (string, error) {
		return e.askHuman(qCtx, task.ID, run.ID, question)
	}))

	registry.Register(tools.NewUpdateTaskDetails(func(uCtx context.Context, refinedDescription, acceptanceCriteria, testCases string) error {
		t, err := e.q.GetTask(uCtx, task.ID)
		if err != nil {
			return err
		}
		if refinedDescription != "" {
			t.RefinedDescription = refinedDescription
		}
		if acceptanceCriteria != "" {
			t.AcceptanceCriteria = acceptanceCriteria
		}
		if testCases != "" {
			t.TestCases = testCases
		}
		if _, err := e.q.UpdateTask(uCtx, t); err != nil {
			return err
		}
		e.hub.BroadcastEvent("task_updated", map[string]interface{}{
			"id":                  task.ID,
			"status":              t.Status,
			"refined_description": t.RefinedDescription,
			"acceptance_criteria": t.AcceptanceCriteria,
			"test_cases":          t.TestCases,
		})
		e.logInfo(proxyLogger, "Task details updated (refined description / acceptance criteria / test cases)")
		return nil
	}))

	registry.Register(tools.NewReportStatus(func(sCtx context.Context, status string) error {
		if err := e.q.UpdateRunCurrentStatus(sCtx, run.ID, status); err != nil {
			return err
		}
		e.hub.BroadcastEvent("run_status", map[string]interface{}{"run_id": run.ID, "task_id": task.ID, "status": status})
		e.logInfo(proxyLogger, "Status: "+status)
		return nil
	}))
	registry.Register(tools.NewExpandRunResult(func(rCtx context.Context, runID int32) (string, error) {
		r, err := e.q.GetRun(rCtx, runID)
		if err != nil {
			return "", err
		}
		if r.ResultDescription == "" {
			return "No detailed explanation available for this run.", nil
		}
		return r.ResultDescription, nil
	}))

	// Build the MCP session store for external integrations.
	accountIDByName := make(map[string]int32)
	serverIDByName := make(map[string]int32)
	onAuthError := func(serverName, rawErr string) {
		accID, ok := accountIDByName[serverName]
		if !ok {
			return
		}
		msg := "Auth token invalid or expired. Re-authenticate."
		if strings.Contains(strings.ToLower(rawErr), "forbidden") ||
			strings.Contains(strings.ToLower(rawErr), "permission denied") {
			msg = "Permission denied. Check your auth token has the required scopes."
		}
		_ = e.q.UpdateMCPAccountLastError(context.Background(), accID, msg)
	}
	onToolCall := func(serverName, toolName string) {
		if srvID, ok := serverIDByName[serverName]; ok {
			_ = e.q.IncrementMCPToolCallCount(context.Background(), srvID, toolName)
		}
	}
	store := tools.NewMCPSessionStore(nil, onAuthError, onToolCall)

	callTool, discoverTool := tools.NewMCPTools(store)
	registry.Register(callTool)
	registry.Register(discoverTool)

	// Wire codegraph proxy: one MCP server process per project, project names
	// exposed as an enum on every codegraph tool call.
	if cgServers, cgErr := e.q.ListCodegraphProjectServers(ctx, task.CompanyID); cgErr == nil && len(cgServers) > 0 {
		// Filter out servers explicitly disabled by this agent.
		if agentCGAssign, err := e.q.GetAgentCodegraphAssignments(ctx, agent.ID); err == nil && len(agentCGAssign) > 0 {
			filtered := make([]db.CodegraphProjectServer, 0, len(cgServers))
			for _, s := range cgServers {
				if enabled, explicit := agentCGAssign[s.Server.ID]; !explicit || enabled {
					filtered = append(filtered, s)
				}
			}
			cgServers = filtered
		}
		if len(cgServers) > 0 {
			var currentProj *db.Project
			if task.ProjectID != nil {
				if p, pErr := e.q.GetProject(ctx, *task.ProjectID); pErr == nil {
					currentProj = &p
				}
			}
			cgProxy := tools.NewCodegraphProxy(currentProj, cgServers)
			cgProxy.RegisterAll(registry)
			defer cgProxy.Close()
			e.logInfo(proxyLogger, fmt.Sprintf("Codegraph: %d project(s) available", len(cgServers)))
		}
	} else if cgErr != nil {
		e.logInfo(proxyLogger, fmt.Sprintf("Warning: failed to load codegraph servers: %v", cgErr))
	}

	// Apply tool filter from agent config (if set). An empty AllowedTools means all tools.
	if agentCfg != nil && len(agentCfg.AllowedTools) > 0 {
		registry = registry.Filter(agentCfg.AllowedTools)
	}

	// MCP listing token costs — set if any external MCP servers are active for this run.
	var listingCostTotal int
	var listingCostByServer map[string]int

	// Load MCP accounts enabled for this agent and register external servers in the store.
	if accounts, mcpErr := e.q.ListMCPAccountsForAgent(ctx, agent.ID); mcpErr == nil && len(accounts) > 0 {
		allServers, _ := e.q.ListMCPServers(ctx, 0) // 0 = all companies
		serverByID := make(map[int32]db.MCPServer, len(allServers))
		for _, s := range allServers {
			serverByID[s.ID] = s
		}
		// Load per-agent, per-server tool filters.
		toolFilters, _ := e.q.GetAgentMCPToolFilters(ctx, agent.ID)
		for _, acc := range accounts {
			srv, ok := serverByID[acc.MCPServerID]
			if !ok || srv.Transport == "builtin" {
				continue
			}
			// Honour AllowedMCPs filter from agent config.
			if agentCfg != nil && len(agentCfg.AllowedMCPs) > 0 {
				allowed := false
				for _, name := range agentCfg.AllowedMCPs {
					if name == srv.Name {
						allowed = true
						break
					}
				}
				if !allowed {
					continue
				}
			}
			synthetic := srv
			synthetic.AuthToken = acc.AuthToken
			synthetic.Name = fmt.Sprintf("%s/%s", srv.Name, acc.Name)
			store.AddExternalServer(synthetic)
			accountIDByName[synthetic.Name] = acc.ID
			serverIDByName[synthetic.Name] = synthetic.ID
			// Apply per-tool filters: build a disabled map for this server.
			if serverFilters, ok := toolFilters[srv.ID]; ok {
				disabledMap := make(map[string]bool, len(serverFilters))
				for toolName, enabled := range serverFilters {
					if !enabled {
						disabledMap[toolName] = true
					}
				}
				if len(disabledMap) > 0 {
					store.SetDisabledTools(synthetic.Name, disabledMap)
				}
			}
		}
		mcpNames := store.ServerNames()
		if len(mcpNames) > 0 {
			e.logInfo(proxyLogger, "MCP: "+strings.Join(mcpNames, ", "))
			listing := store.CompactListing()
			systemPrompt += listing
			listingCostByServer = store.ListingCostByServer()
			for _, c := range listingCostByServer {
				listingCostTotal += c
			}
		}
	} else if mcpErr != nil {
		e.logInfo(proxyLogger, fmt.Sprintf("Warning: failed to load MCP accounts for agent: %v", mcpErr))
	}

	// Determine agent mode and reasoning level from config.
	agentMode := aicli.ModeMessageHistory
	reasoningLevel := ""
	if agentCfg != nil {
		switch agentCfg.ChatType {
		case agentconfig.ChatTypeCompactThinking:
			agentMode = aicli.ModeCompactThinking
		}
		reasoningLevel = string(agentCfg.ReasoningLevel)
	}

	// Wire the proxy logger as the agent's RunLogger so request/response entries
	// appear in the log file and the DB (identical format to the gateway).
	llmClient := aicli.NewClient(provider.BaseUrl, provider.ApiKey, model)
	agentCfgObj := aicli.Config{
		Client:                llmClient,
		Registry:              registry,
		Mode:                  agentMode,
		ProviderName:          provider.Name,
		AgentName:             agent.Name,
		ReasoningLevel:        reasoningLevel,
		MCPListingCostPerTurn: listingCostTotal,
		MCPServerListingCosts: listingCostByServer,
		Queries:               e.q,
		RunID:                 run.ID,
		Logger:                proxyLogger,
	}
	aiAgent := aicli.New(agentCfgObj)

	e.logInfo(proxyLogger, fmt.Sprintf("Starting native agent for task %d (mode=%s model=%s provider=%s)", task.ID, mode, model, provider.Name))
	e.logInfo(proxyLogger, fmt.Sprintf("Workspace: %s", workspacePath))
	if agentCfg != nil {
		e.logInfo(proxyLogger, fmt.Sprintf("Agent config: %s (chat_type=%s reasoning=%s)", agentCfg.Name, agentCfg.ChatType, agentCfg.ReasoningLevel))
	}

	_, agentErr := aiAgent.RunWithMessages(runCtx, systemPrompt, initialMessages)

	status := "completed"

	if agentErr != nil {
		if runCtx.Err() == context.Canceled {
			e.logInfo(proxyLogger, "Run canceled by user")
			e.q.UpdateRunLog(context.Background(), run.ID, "", "canceled")
			e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": "canceled"})
			e.notifyParentOfSubtaskCompletion(context.Background(), task, "canceled")
			return "canceled"
		}
		status = "failed"
		e.logError(proxyLogger, fmt.Sprintf("Agent error: %v", agentErr))
	}


	// If finish_task was not called, force a follow-up turn.
	if agentErr == nil && !taskFinished {
		e.logInfo(proxyLogger, "finish_task not called. Sending follow-up to force it.")
		_, followErr := aiAgent.Run(runCtx, systemPrompt,
			"You must call finish_task before ending. Choose the appropriate status: 'in-review' if done, 'blocked' if stuck, 'done' if fully complete, or 'refinement' if you need clarification. Provide a short one-sentence finish_status.")
		if followErr != nil {
			e.logError(proxyLogger, fmt.Sprintf("Follow-up failed: %v", followErr))
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
	if updatedRun, err := e.q.GetRun(ctx, run.ID); err == nil {
		storage.WriteRun(updatedRun, company.ShortName)
	}

	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": status})

	// Notify the parent task that this subtask has completed or failed.
	e.notifyParentOfSubtaskCompletion(ctx, task, status)

	return status
}

// askHuman posts the agent's question as an ask_user comment and blocks until
// a human replies on the task, returning the reply text. It keeps the run's
// last_message_time fresh while waiting so the run isn't declared stale.
func (e *NativeEngine) askHuman(ctx context.Context, taskID, runID int32, question string) (string, error) {
	rid := runID
	questionComment, err := e.q.CreateComment(ctx, db.Comment{
		TaskID:      taskID,
		AuthorType:  "agent",
		CommentType: "ask_user",
		Content:     question,
		RunID:       &rid,
	})
	if err != nil {
		return "", fmt.Errorf("ask_human: failed to post question: %w", err)
	}
	e.hub.BroadcastEvent("comment_created", questionComment)
	e.hub.BroadcastEvent("human_input_requested", map[string]interface{}{
		"task_id":  taskID,
		"run_id":   runID,
		"question": question,
	})

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
		// Keep the run alive so the staleness detector doesn't kill it while
		// we wait for the user.
		e.q.TouchRunLastMessageTime(context.Background(), runID)

		comments, listErr := e.q.ListCommentsByTask(context.Background(), taskID)
		if listErr != nil {
			continue
		}
		for _, c := range comments {
			if c.AuthorType == "human" && c.ID > questionComment.ID {
				return c.Content, nil
			}
		}
	}
}

// makeDelegateFunc returns the callback behind the delegate_task tool. It
// creates a child task, runs it synchronously as a nested session linked to
// the parent run, and returns the specialist's result to the delegating agent.
func (e *NativeEngine) makeDelegateFunc(
	parentTask db.Task,
	parentAgent db.Agent,
	parentRun db.Run,
	parentLogger *logging.ProxyLogger,
	rootRunID, rootTaskID int32,
	workspacePath string,
	depth int,
) func(callCtx context.Context, title, description, agentName string) (string, error) {
	return func(callCtx context.Context, title, description, agentName string) (string, error) {
		// Reject if another subtask of this parent is already running.
		runningCount, err := e.q.CountRunningSubtasks(callCtx, parentTask.ID)
		if err != nil {
			return "", fmt.Errorf("failed to check running subtasks: %w", err)
		}
		if runningCount > 0 {
			return "", fmt.Errorf("a delegated session is already running for task %d; wait for it to complete", parentTask.ID)
		}

		if e.agentFactory == nil {
			return "", fmt.Errorf("no agent configs available for delegation")
		}
		if _, cfgErr := e.agentFactory.GetConfig(agentName); cfgErr != nil {
			return "", fmt.Errorf("unknown agent config %q: %w", agentName, cfgErr)
		}

		parentID := parentTask.ID
		agentID := parentAgent.ID
		subtask, err := e.q.CreateTask(callCtx, db.Task{
			CompanyID:       parentTask.CompanyID,
			ProjectID:       parentTask.ProjectID,
			SprintID:        parentTask.SprintID,
			AgentID:         &agentID,
			ParentID:        &parentID,
			Title:           title,
			Description:     description,
			TaskType:        db.TaskTypeImplement,
			Status:          "in-progress",
			Priority:        "Normal",
			AgentConfigName: agentName,
		})
		if err != nil {
			return "", fmt.Errorf("failed to create subtask: %w", err)
		}

		e.hub.BroadcastEvent("task_created", map[string]interface{}{
			"id":        subtask.ID,
			"parent_id": parentTask.ID,
			"title":     subtask.Title,
		})

		var childRunID int32
		session := &parentSession{
			parentRunID:   parentRun.ID,
			rootRunID:     rootRunID,
			rootTaskID:    rootTaskID,
			workspacePath: workspacePath,
			depth:         depth + 1,
			onRunCreated: func(childRun db.Run) {
				childRunID = childRun.ID
				if parentLogger != nil {
					parentLogger.LogSessionStarted(childRun.ID, subtask.ID, agentName, title, fmt.Sprintf("session-%d.log", childRun.ID))
				}
				e.hub.BroadcastEvent("session_started", map[string]interface{}{
					"parent_run_id": parentRun.ID,
					"run_id":        childRun.ID,
					"task_id":       subtask.ID,
					"agent_name":    agentName,
					"title":         title,
				})
			},
		}

		status := e.executeSession(callCtx, subtask, "implement", session)

		result := ""
		if childRunID > 0 {
			if childRun, runErr := e.q.GetRun(context.Background(), childRunID); runErr == nil {
				result = childRun.ResultDescription
			}
		}
		if parentLogger != nil {
			parentLogger.LogSessionEnded(childRunID, status, result)
		}
		e.hub.BroadcastEvent("session_ended", map[string]interface{}{
			"parent_run_id": parentRun.ID,
			"run_id":        childRunID,
			"status":        status,
		})

		finalTask, taskErr := e.q.GetTask(context.Background(), subtask.ID)
		taskStatus := subtask.Status
		if taskErr == nil {
			taskStatus = finalTask.Status
		}
		if result == "" {
			result = "(no result summary provided)"
		}
		return fmt.Sprintf("Delegated session for subtask #%d (%s) finished.\nSession status: %s\nSubtask status: %s\nResult: %s",
			subtask.ID, agentName, status, taskStatus, result), nil
	}
}

// notifyParentOfSubtaskCompletion adds a comment to the parent task when this
// subtask finishes, so the parent agent can react to the result.
func (e *NativeEngine) notifyParentOfSubtaskCompletion(ctx context.Context, subtask db.Task, status string) {
	if subtask.ParentID == nil {
		return
	}
	msg := fmt.Sprintf("Subtask #%d %q completed with status: %s.", subtask.ID, subtask.Title, status)
	comment, err := e.q.CreateComment(ctx, db.Comment{
		TaskID:     *subtask.ParentID,
		AuthorType: "system",
		Content:    msg,
	})
	if err != nil {
		fmt.Printf("Warning: failed to notify parent task %d of subtask completion: %v\n", *subtask.ParentID, err)
		return
	}
	e.hub.BroadcastEvent("comment_created", comment)
	e.hub.BroadcastEvent("subtask_completed", map[string]interface{}{
		"subtask_id":  subtask.ID,
		"parent_id":   *subtask.ParentID,
		"status":      status,
		"subtask_title": subtask.Title,
	})
}

// emitStatusChange creates a status_change comment and broadcasts it.
func (e *NativeEngine) emitStatusChange(ctx context.Context, taskID int32, from, to string) {
	content, _ := json.Marshal(map[string]string{"from": from, "to": to})
	comment, err := e.q.CreateComment(ctx, db.Comment{
		TaskID:      taskID,
		AuthorType:  "system",
		CommentType: "status_change",
		Content:     string(content),
	})
	if err == nil {
		e.hub.BroadcastEvent("comment_created", comment)
	}
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

