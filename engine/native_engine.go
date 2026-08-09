package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/git"
	"agent-orchestrator/pkg/githubapp"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"

	"gorm.io/gorm"
)

const runStatusCompleted = "completed"

func taskGitBranch(taskID int32) string {
	return fmt.Sprintf("headcount1/task-%d", taskID)
}

// NativeEngine implements Engine using the aicli package for direct LLM communication.
type NativeEngine struct {
	q            *db.Queries
	hub          *eventhub.Hub
	agentFactory agentconfig.Factory
	cancelFuncs  sync.Map // runID -> context.CancelFunc

	// draining and activeRoots back BeginDrain/WaitForActiveRuns: the
	// graceful-shutdown path for an auto-update. draining, once set, stops
	// processTask from starting new runs and asks every active ROOT run's
	// agent loop to pause at its next safe turn boundary instead of
	// continuing (see executeSession). Only root runs are tracked/paused —
	// a run with an active delegation tree runs it to completion rather than
	// attempting to pause child sessions, whose parent-child coordination
	// (Go channels) cannot survive a process restart; see executeSession.
	draining    atomic.Bool
	activeRoots sync.WaitGroup
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

// BeginDrain flips the engine into drain mode for a graceful shutdown (e.g.
// applying an auto-update): processTask stops starting new runs, and every
// active root run is asked to pause at its next safe turn boundary — right
// after its current in-flight LLM call returns — instead of continuing.
// Idempotent; safe to call more than once.
func (e *NativeEngine) BeginDrain() {
	e.draining.Store(true)
}

// WaitForActiveRuns blocks until every active root run has either finished
// normally or paused (see BeginDrain), or ctx's deadline passes — whichever
// comes first. Runs still active when the deadline passes are abandoned (the
// process is about to exit); they are recovered as ordinary crashed runs by
// the existing stale-run cleanup on the next boot, exactly as an ungraceful
// kill -9 would leave them today.
func (e *NativeEngine) WaitForActiveRuns(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		e.activeRoots.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// defaultOrchestratorConfig is the agent config every root task is routed
// through: the CEO orchestrates execution via delegation to specialists.
const defaultOrchestratorConfig = "CEO"

// maxDelegationDepth caps how deep delegation sessions can nest: the main
// task (depth 0, CEO) and first-level subtasks (depth 1, e.g. CTO/CMO) can
// create subtasks; deeper sessions (depth 2, the implementers) cannot.
const maxDelegationDepth = 2

// parentSession links a delegated child session to the session that spawned
// it: run hierarchy ids, the parent's workspace, a hook that fires once the
// child's run record exists (so the parent can log the session start), and
// the askOwner callback backing the child's ask_task_owner tool.
type parentSession struct {
	parentRunID   int32
	rootRunID     int32
	rootTaskID    int32
	workspacePath string
	depth         int
	onRunCreated  func(run db.Run)
	askOwner      func(ctx context.Context, question string) (string, error)
}

// subtaskEvent is what a running subtask session reports back to the waiting
// owner session: either a question from the sub-agent (via ask_task_owner) or
// the session's final status.
type subtaskEvent struct {
	question string
	status   string
	done     bool
}

// delegationState tracks one running subtask session so the owner session can
// wait for its completion and exchange question/answer messages with it.
type delegationState struct {
	subtaskID  int32
	agentName  string
	title      string
	childRunID int32
	eventCh    chan subtaskEvent
	answerCh   chan string
}

// pendingSubtasks holds the delegation states whose sub-agent is paused on an
// unanswered ask_task_owner question, keyed by subtask id.
type pendingSubtasks struct {
	mu sync.Mutex
	m  map[int32]*delegationState
}

func (p *pendingSubtasks) put(state *delegationState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[state.subtaskID] = state
}

// take removes and returns the state for the given subtask id. A zero id is
// accepted when exactly one question is pending.
func (p *pendingSubtasks) take(subtaskID int32) (*delegationState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.m) == 0 {
		return nil, fmt.Errorf("no subtask has a pending question")
	}
	if subtaskID == 0 {
		if len(p.m) == 1 {
			for id, state := range p.m {
				delete(p.m, id)
				return state, nil
			}
		}
		return nil, fmt.Errorf("multiple subtasks have pending questions — pass subtask_id explicitly")
	}
	state, ok := p.m[subtaskID]
	if !ok {
		ids := make([]string, 0, len(p.m))
		for id := range p.m {
			ids = append(ids, fmt.Sprintf("#%d", id))
		}
		return nil, fmt.Errorf("subtask %d has no pending question (pending: %s)", subtaskID, strings.Join(ids, ", "))
	}
	delete(p.m, subtaskID)
	return state, nil
}

// ProcessTask reacts to a task's current status and spawns a goroutine to run
// the agent when that status implies pending work ("to-do", "in-progress").
// Tasks in terminal or manual statuses (in-review, blocked, done, refinement)
// are left untouched — moving a card to "done" must not restart the agent.
func (e *NativeEngine) ProcessTask(ctx context.Context, taskID int32) error {
	return e.processTask(ctx, taskID, false)
}

// RerunTask forces a new agent run for an explicit user action (the Re-run
// button or a comment with the Run Agent flag), moving the task from a
// terminal status back to "in-progress" if necessary.
func (e *NativeEngine) RerunTask(ctx context.Context, taskID int32) error {
	return e.processTask(ctx, taskID, true)
}

func (e *NativeEngine) processTask(ctx context.Context, taskID int32, forceRerun bool) error {
	// Draining for a graceful shutdown (see BeginDrain): refuse to start any
	// new run. Existing runs already in flight are left to reach their own
	// pause point; this task will be picked up again on the next boot, either
	// via its resumed run or (once that completes) the next natural status
	// transition.
	if e.draining.Load() {
		return nil
	}

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
			e.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", map[string]interface{}{"id": task.ID, "status": "in-progress"})
			e.emitStatusChange(ctx, task.ID, prevStatus, "in-progress")
			go e.run(context.Background(), task, "implement")
		} else {
			prevStatus := task.Status
			task.Status = "refinement"
			if _, err := e.q.UpdateTask(ctx, task); err != nil {
				return err
			}
			e.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", map[string]interface{}{"id": task.ID, "status": "refinement"})
			e.emitStatusChange(ctx, task.ID, prevStatus, "refinement")
			go e.run(context.Background(), task, "plan")
		}
	case "in-progress":
		go e.run(context.Background(), task, "implement")
	case "in-review", "blocked", "done", "refinement":
		// Only an explicit re-run (Re-run button, Run Agent comment) may pull
		// a task out of these statuses; a plain status change never does.
		if !forceRerun {
			return nil
		}
		prevStatus := task.Status
		task.Status = "in-progress"
		if _, err := e.q.UpdateTask(ctx, task); err != nil {
			return err
		}
		e.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", map[string]interface{}{"id": task.ID, "status": "in-progress"})
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
	e.broadcastForTask(ctx, run.TaskID, "run_ended", map[string]interface{}{"run_id": runID, "status": "failed"})
	e.q.UnlockTaskRun(ctx, run.TaskID)
}

// run is the goroutine body for a single root agent execution.
func (e *NativeEngine) run(ctx context.Context, task db.Task, mode string) {
	e.executeSession(ctx, task, mode, nil, nil)
}

// resumeSession re-enters a previously-paused root run using its persisted
// conversation history instead of building a fresh one from the task. Only
// root runs are ever paused (see BeginDrain/executeSession's pause wiring),
// so this always passes parent = nil.
func (e *NativeEngine) resumeSession(ctx context.Context, task db.Task, run db.Run) {
	e.executeSession(ctx, task, "resume", nil, &run)
}

// ResumeInterruptedRuns re-enters every run left paused by a graceful
// shutdown (see BeginDrain), picking up its conversation exactly where it
// left off. Call once at boot, after the engine is constructed. A run whose
// task/agent has since disappeared, or whose saved history fails to parse,
// is marked failed and unlocked instead — the same fallback ordinary
// stale-run recovery uses for a hard crash.
func (e *NativeEngine) ResumeInterruptedRuns(ctx context.Context) {
	runs, err := e.q.GetInterruptedRuns(ctx)
	if err != nil {
		fmt.Printf("Warning: failed to list interrupted runs: %v\n", err)
		return
	}
	if len(runs) == 0 {
		return
	}
	fmt.Printf("Resuming %d interrupted run(s) after restart...\n", len(runs))
	for _, run := range runs {
		task, taskErr := e.q.GetTask(ctx, run.TaskID)
		if taskErr != nil {
			fmt.Printf("Warning: could not resume run %d: task %d not found: %v\n", run.ID, run.TaskID, taskErr)
			e.resolveStaleRun(ctx, run.ID)
			continue
		}
		if resumeErr := e.q.ResumeRun(ctx, run.ID); resumeErr != nil {
			fmt.Printf("Warning: failed to mark run %d as running for resume: %v\n", run.ID, resumeErr)
			continue
		}
		go e.resumeSession(context.Background(), task, run)
	}
}

// executeSession runs one agent session for a task and returns its final run
// status ("completed", "failed", "canceled" or "interrupted"). Root sessions
// (parent == nil) run detached from the caller's context; delegated child
// sessions inherit the parent's run context so stopping the parent stops the
// whole tree.
//
// resumeRun, when non-nil, re-enters a previously paused root run instead of
// starting a fresh one: its persisted conversation (PausedHistory) seeds the
// agent loop in place of a freshly built initial message list, and the
// existing Run row is reused rather than creating a new one. Only root runs
// are ever resumed — see the pause-signal wiring below.
func (e *NativeEngine) executeSession(ctx context.Context, task db.Task, mode string, parent *parentSession, resumeRun *db.Run) string {
	if task.AgentID == nil {
		return "failed"
	}

	// Delegated child tasks arrive fresh from CreateTask without preloaded
	// associations (Company, Project, Sprint) — reload so the system prompt
	// and artifact paths see the full task.
	if full, err := e.q.GetTask(ctx, task.ID); err == nil {
		task = full
	}

	agent, err := e.q.GetAgent(ctx, *task.AgentID)
	if err != nil {
		return "failed"
	}

	var run db.Run
	if resumeRun != nil {
		run = *resumeRun
	} else {
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
		created, createErr := e.q.CreateRun(ctx, newRun)
		if createErr != nil {
			return "failed"
		}
		run = created
		if parent == nil {
			// Root runs point at themselves so the whole tree shares one root id.
			rootID := run.ID
			run.RootRunID = &rootID
			if rootErr := e.q.SetRunRootID(ctx, run.ID, rootID); rootErr != nil {
				fmt.Printf("Warning: failed to set root run id for run %d: %v\n", run.ID, rootErr)
			}
		}
	}

	// Track active root runs so a graceful shutdown (BeginDrain +
	// WaitForActiveRuns) knows when it's safe to proceed. A root run's
	// goroutine only returns once its whole delegation tree (if any) has
	// finished, so tracking roots alone covers entire trees.
	if parent == nil {
		e.activeRoots.Add(1)
		defer e.activeRoots.Done()
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

	// LockTaskRun is a conditional UPDATE (WHERE run_id IS NULL): for a fresh
	// run it claims the task; for a resumed run the task is already locked to
	// this same run ID (pausing never unlocks it — see the paused branch
	// below), so this is a harmless no-op that leaves the existing lock as-is.
	if lockErr := e.q.LockTaskRun(ctx, task.ID, run.ID); lockErr != nil {
		fmt.Printf("Warning: failed to lock task %d for run %d: %v\n", task.ID, run.ID, lockErr)
	}
	// paused is set just before returning if this session stops via
	// aicli.ErrPaused. In that case the task must stay locked to this run (no
	// other run may start on it) until ResumeInterruptedRuns picks it back up
	// after the restart, so the unlock below is skipped.
	paused := false
	defer func() {
		if paused {
			return
		}
		if clearErr := e.q.UnlockTaskRun(context.Background(), task.ID); clearErr != nil {
			fmt.Printf("Warning: failed to unlock task %d: %v\n", task.ID, clearErr)
		}
	}()

	// Let the delegating session record the child run before any slow work.
	if parent != nil && parent.onRunCreated != nil {
		parent.onRunCreated(run)
	}

	// A resumed run re-enters "running", so it reuses the same run_started
	// event a fresh run emits: the Run Logs UI re-fetches the list on it (the
	// run reappears as active) and no consumer has to learn a new event type.
	e.hub.BroadcastEventForCompany(task.CompanyID, "run_started", run)

	company, compErr := e.q.GetCompany(ctx, task.CompanyID)
	if compErr != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("failed to get company: %v", compErr))
		return "failed"
	}

	settings := loadSettings()

	// Session hierarchy: which run/task the log folder is grouped under.
	rootRunID := run.ID
	rootTaskID := task.ID
	depth := 0
	if parent != nil {
		rootRunID = parent.rootRunID
		rootTaskID = parent.rootTaskID
		depth = parent.depth
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

	// Resolve the LLM target. Agents bound to a model group talk to the
	// in-process group router (free-first ordering, failover, stats) through
	// a synthetic provider pointing at the local gateway; otherwise the
	// agent's fixed provider+model is used directly.
	groupMode := agent.ModelGroupID != nil
	provider, model, err := resolveProvider(ctx, e.q, agent, agentCfg)
	if err != nil {
		e.failRun(ctx, run.ID, err.Error())
		return "failed"
	}

	// Assign the human-readable run name: "<task ref>-<AGENTSHORT>[-n]",
	// e.g. "DEC-50-CEO", "DEC-50-2-QA-2". A resumed run already has its name
	// from before it paused — recomputing would double-count it against
	// CountRunsByNameKey (its own row now matches the key) and rename it.
	if resumeRun == nil {
		shortName := agentconfig.DeriveShortName(agent.Name)
		if agentCfg != nil {
			shortName = agentCfg.EffectiveShortName()
		}
		taskRef := task.RefKey
		if taskRef == "" {
			taskRef = fmt.Sprintf("TASK-%d", task.ID)
		}
		runKey := taskRef + "-" + shortName
		// prior = earlier runs with this key (this run's name is still empty, so
		// it is not counted). The second run becomes "<key>-2", and so on.
		if prior, cErr := e.q.CountRunsByNameKey(ctx, task.ID, runKey); cErr == nil && prior > 0 {
			runKey = fmt.Sprintf("%s-%d", runKey, prior+1)
		}
		run.Name = runKey
		if nErr := e.q.UpdateRunName(ctx, run.ID, runKey); nErr != nil {
			fmt.Printf("Warning: failed to store run name for run %d: %v\n", run.ID, nErr)
		}
	}

	// Workspace layout:
	//   - the main task and every delegated subtask get their own directory
	//     (a git worktree when the task belongs to a repo-backed project)
	//   - delegated subtask sessions can additionally READ the parent task's
	//     workdir, but never write to it
	fsMgr := filesystem.NewManager(settings.BasePath)
	workspacePath := fsMgr.GetTaskWorktreePath(company, task)
	var readOnlyDirs []string
	if parent != nil && parent.workspacePath != "" && parent.workspacePath != workspacePath {
		readOnlyDirs = append(readOnlyDirs, parent.workspacePath)
	}

	// Set up the session logger. All sessions of one main run share the
	// data/{company}/logs/{rootTaskID}/run-{rootRunID}/ folder: the root
	// session writes main.jsonl, child sessions write session-{runID}.jsonl.
	proxyLogger, logErr := logging.NewSessionLoggerWithHub(
		settings.BasePath,
		company.ShortName,
		rootTaskID,
		rootRunID,
		run.ID,
		e.hub.ForCompany(task.CompanyID),
		e.q,
	)
	if logErr != nil {
		fmt.Printf("Warning: failed to create proxy logger: %v\n", logErr)
	} else {
		defer proxyLogger.Close()
		e.q.UpdateRunLogFilePath(ctx, run.ID, proxyLogger.FilePath())
	}

	// Git worktree setup. Every session (main task and delegated subtasks)
	// works inside a git worktree of the project repo on its own task-N
	// branch and commits its changes at the end of the session.
	var gitProject bool
	var gitMgr *git.GitManager
	if task.ProjectID != nil {
		project, projErr := e.q.GetProject(ctx, *task.ProjectID)
		if projErr == nil && project.RepositoryUrl != "" {
			gitProject = true
			projectRepoDir := fsMgr.GetProjectRepoPath(company, project)
			// The worktree's .git points back into the project repo's object
			// store, so git commands in the workspace read {projectRepoDir}/.git.
			readOnlyDirs = append(readOnlyDirs, projectRepoDir)
			keyPath, keyCleanup := filesystem.ResolveSSHKeyPathForCompany(ctx, e.q, settings.BasePath, company)
			defer keyCleanup()
			gitMgr = git.NewGitManager(projectRepoDir, keyPath)
			if project.GitHubInstallationID != 0 {
				if token, tokenErr := githubapp.TokenForProject(ctx, project); tokenErr == nil && token != "" {
					gitMgr.WithHTTPToken(token)
				} else if tokenErr != nil {
					e.logInfo(proxyLogger, "GitHub App token error: "+tokenErr.Error())
				}
			}
			if pullErr := gitMgr.Pull(ctx); pullErr != nil {
				e.logInfo(proxyLogger, "Warning: git pull failed: "+pullErr.Error())
			}
			// CreateTaskWorkspace creates an empty directory before the run; it
			// still needs converting into a Git worktree.
			if _, statErr := os.Stat(filepath.Join(workspacePath, ".git")); os.IsNotExist(statErr) {
				branchName := taskGitBranch(task.ID)
				// Git refuses to add a worktree into the placeholder directory
				// created with the task. It contains only disposable task memory.
				_ = os.RemoveAll(workspacePath)
				if wtErr := gitMgr.CreateWorktree(ctx, projectRepoDir, workspacePath, branchName, "origin/"+task.EffectiveGitBaseBranch()); wtErr != nil {
					e.logInfo(proxyLogger, "Failed to create worktree: "+wtErr.Error())
					gitProject = false
				}
			}
		}
	}

	// Ensure the workdir exists (no-op when the worktree was just created)
	// and seed the task memory file.
	if err := os.MkdirAll(workspacePath, 0755); err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("failed to create workspace: %v", err))
		return "failed"
	}
	if err := initTaskMemory(workspacePath, task, company); err != nil {
		fmt.Printf("Warning: failed to init memory.md: %v\n", err)
	}

	// Artifact (deliverable) directory: {basePath}/artifacts/{company}/{rootTaskID}.
	// Always keyed by the root task so it matches ListArtifactsByTaskTree —
	// every session of one execution tree shares the same deliverables dir.
	artifactDir := fsMgr.Paths().TaskArtifactsDir(company.ShortName, rootTaskID)
	// Artifact files are readable by every session's file tools (the CEO has
	// no file tools, so it only ever sees the metadata list below).
	readOnlyDirs = append(readOnlyDirs, artifactDir)

	// Build system prompt.
	var systemPrompt string
	if agentCfg != nil && agentCfg.Prompt != "" {
		// Use config prompt as the base; append task context from the builder.
		taskContext := NewSystemPromptBuilder(e.q).Build(agent, task)
		systemPrompt = agentCfg.Prompt + "\n\n" + taskContext
	} else {
		systemPrompt = NewSystemPromptBuilder(e.q).Build(agent, task)
	}
	systemPrompt += fmt.Sprintf("\nWorkdir: %s", workspacePath)
	if len(readOnlyDirs) > 0 {
		systemPrompt += fmt.Sprintf("\nReadable (read-only) dirs: %s", strings.Join(readOnlyDirs, ", "))
	}

	// Every session sees the artifact list of the whole task tree — metadata
	// only (name, size, lines, modify time, description, verified flag).
	// Agents with file tools can read the files from the artifacts dir.
	if arts, artErr := e.q.ListArtifactsByTaskTree(ctx, rootTaskID); artErr == nil && len(arts) > 0 {
		systemPrompt += fmt.Sprintf("\n\nArtifacts produced so far (%d, files in %s):\n%s", len(arts), artifactDir, formatArtifactList(arts))
	}

	initialMessages := e.buildInitialMessages(ctx, task, mode)

	// Build full tool registry: file/shell/web tools + task-management tools.
	registry := tools.DefaultRegistry(workspacePath, readOnlyDirs...)

	// Track whether finish_task was called so we can force it if not, plus
	// the agent's own verdict and summary for the run's outcome log entry.
	var taskFinished bool
	var finishResult tools.FinishTaskResult
	// The model-group gateway token belongs to the whole execution session.
	// Nested LLM calls must reuse it; issuing another token for the same run
	// invalidates this one in the registry.
	var gatewayAuth runGatewayAuth
	gatewayAuth.runID = run.ID

	registry.Register(tools.NewFinishTask(parent != nil, func(finCtx context.Context, result tools.FinishTaskResult) error {
		t, err := e.q.GetTask(finCtx, task.ID)
		if err != nil {
			return err
		}
		taskFinished = true
		finishResult = result
		prevStatus := t.Status
		t.Status = result.Status
		if _, err := e.q.UpdateTask(finCtx, t); err != nil {
			return err
		}
		e.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", map[string]interface{}{"id": task.ID, "status": result.Status})
		if err := e.q.UpdateRunResult(finCtx, run.ID, result.FinishStatus, result.ResultDetails); err != nil {
			fmt.Printf("Warning: failed to store run result: %v\n", err)
		}
		runID := run.ID
		content, _ := json.Marshal(map[string]string{"msg": result.FinishStatus, "from": prevStatus, "to": result.Status})
		comment, cErr := e.q.CreateComment(finCtx, db.Comment{
			TaskID:      task.ID,
			AuthorType:  "agent",
			CommentType: "task_done",
			Content:     string(content),
			RunID:       &runID,
		})
		if cErr == nil {
			e.hub.BroadcastEventForCompany(task.CompanyID, "comment_created", comment)
		}
		return nil
	}))

	registry.Register(tools.NewWriteArtifactFile(func(wCtx context.Context, filename, content, description string) (string, error) {
		// Artifacts are flat files in the shared artifacts dir: a filename
		// with path separators (or "..") could escape it.
		if filename != filepath.Base(filename) || filename == "." || filename == ".." {
			return "", fmt.Errorf("invalid artifact filename %q — use a plain filename without directories", filename)
		}
		if err := os.MkdirAll(artifactDir, 0755); err != nil {
			return "", fmt.Errorf("could not create artifact directory: %w", err)
		}
		filePath := filepath.Join(artifactDir, filename)

		// Collision detection across the task tree: overwriting another run's
		// artifact must be visible, not silent (last-write-wins destroyed
		// grounded deliverables in past runs).
		var existing *db.Artifact
		if arts, exErr := e.q.ListArtifactsByTaskTree(wCtx, rootTaskID); exErr == nil {
			for i := len(arts) - 1; i >= 0; i-- {
				if arts[i].Filename == filename {
					existing = &arts[i]
					break
				}
			}
		}

		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("could not write artifact file: %w", err)
		}

		if existing != nil {
			if upErr := e.q.UpdateArtifactContent(wCtx, existing.ID, content, run.ID); upErr != nil {
				fmt.Printf("Warning: failed to update artifact in DB: %v\n", upErr)
			}
			e.hub.BroadcastEventForCompany(task.CompanyID, "artifact_created", *existing)
			if existing.RunID == run.ID {
				return fmt.Sprintf("Artifact %q updated.", filename), nil
			}
			return fmt.Sprintf("Artifact %q written — OVERWROTE an existing artifact originally written by run #%d. "+
				"If that was not intended, use a different filename.", filename, existing.RunID), nil
		}

		artifact, err := e.q.CreateArtifact(wCtx, db.Artifact{
			TaskID:      task.ID,
			RunID:       run.ID,
			Filename:    filename,
			FilePath:    filePath,
			Content:     content,
			Description: description,
		})
		if err != nil {
			fmt.Printf("Warning: failed to save artifact to DB: %v\n", err)
			return "", nil
		}
		e.hub.BroadcastEventForCompany(task.CompanyID, "artifact_created", artifact)
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
			e.hub.BroadcastEventForCompany(task.CompanyID, "comment_created", ac)
		}
		return fmt.Sprintf("Artifact %q written.", filename), nil
	}))

	// Artifact read access: agents can also read deliverables through these
	// DB-backed tools, which work regardless of filesystem sandboxing.
	registry.Register(tools.NewListArtifacts(func(lCtx context.Context) ([]tools.ArtifactInfo, error) {
		artifacts, err := e.q.ListArtifactsByTaskTree(lCtx, rootTaskID)
		if err != nil {
			return nil, err
		}
		infos := make([]tools.ArtifactInfo, 0, len(artifacts))
		for _, a := range artifacts {
			infos = append(infos, tools.ArtifactInfo{
				ID:        a.ID,
				Filename:  a.Filename,
				SizeBytes: len(a.Content),
				WrittenBy: fmt.Sprintf("run #%d", a.RunID),
				UpdatedAt: a.UpdatedAt.Format(time.RFC3339),
			})
		}
		return infos, nil
	}))

	registry.Register(tools.NewReadArtifact(func(rCtx context.Context, filename string) (string, error) {
		arts, err := e.q.ListArtifactsByTaskTree(rCtx, rootTaskID)
		if err != nil {
			return "", err
		}
		for i := len(arts) - 1; i >= 0; i-- {
			if arts[i].Filename == filename {
				return arts[i].Content, nil
			}
		}
		return "", fmt.Errorf("artifact %q not found — call list_artifacts to see what exists", filename)
	}))

	// ask_artifact: verify artifact content through a separate one-shot LLM
	// call — the artifact never enters this session's context, only the short
	// answer does. Uses the configured "ask_artifact" Default Model when set.
	registry.Register(tools.NewAskArtifact(func(aCtx context.Context, filename, question string) (string, error) {
		return e.askArtifact(aCtx, run.ID, rootTaskID, provider, model, gatewayAuth, filename, question, proxyLogger)
	}))
	// Delegation: available until the depth cap (CEO → CTO/CMO → implementers).
	// The set of sub-agents an agent may delegate to comes from its config's
	// Subagents list (falling back to every known config when unset).
	if depth < maxDelegationDepth {
		subagents := []string(nil)
		if agentCfg != nil && len(agentCfg.Subagents) > 0 {
			subagents = agentCfg.Subagents
		} else if e.agentFactory != nil {
			subagents = e.agentFactory.ListNames()
		}
		pending := &pendingSubtasks{m: make(map[int32]*delegationState)}
		registry.Register(tools.NewCreateSubtask(
			e.makeCreateSubtaskFunc(task, agent, run, proxyLogger, rootRunID, rootTaskID, workspacePath, depth, subagents, pending),
			subagents,
		))
		registry.Register(tools.NewAnswerSubtaskQuestion(func(aCtx context.Context, subtaskID int32, answer string) (string, error) {
			state, err := pending.take(subtaskID)
			if err != nil {
				return "", err
			}
			e.recordSubtaskQA(aCtx, state.subtaskID, run.ID, "owner_answer", answer)
			e.logInfo(proxyLogger, fmt.Sprintf("Answered subtask #%d question", state.subtaskID))
			select {
			case state.answerCh <- answer:
			case <-aCtx.Done():
				return "", aCtx.Err()
			}
			return e.waitForSubtaskEvent(aCtx, state, rootTaskID, pending, proxyLogger, run)
		}))
	}

	// create_task: plan new TOP-LEVEL board tasks (gated to the CEO via its
	// tool allowlist). Unlike create_subtask it neither runs nor blocks.
	registry.Register(tools.NewCreateTask(func(cCtx context.Context, p tools.CreateTaskParams) (string, error) {
		return e.createBoardTask(cCtx, task, agent.ID, company, p)
	}))

	registry.Register(tools.NewAskHuman(func(qCtx context.Context, question string) (string, error) {
		return e.askHuman(qCtx, task.ID, run.ID, question)
	}))

	// Delegated sessions can ask the agent that created their subtask a
	// question; the owner session answers via answer_subtask_question.
	if parent != nil && parent.askOwner != nil {
		registry.Register(tools.NewAskTaskOwner(parent.askOwner))
	}

	registry.Register(tools.NewReportStatus(func(sCtx context.Context, status string) error {
		if err := e.q.UpdateRunCurrentStatus(sCtx, run.ID, status); err != nil {
			return err
		}
		e.hub.BroadcastEventForCompany(task.CompanyID, "run_status", map[string]interface{}{"run_id": run.ID, "task_id": task.ID, "status": status})
		e.logInfo(proxyLogger, "Status: "+status)
		return nil
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
		if strings.HasPrefix(serverName, db.MCPServerNameGitHub+"/") {
			msg = "GitHub App authorization failed. Check the app installation permissions and try again."
		}
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
	githubAccountsByServerName := make(map[string]db.MCPAccount)
	var taskProject *db.Project
	if task.ProjectID != nil {
		if project, projectErr := e.q.GetProject(ctx, *task.ProjectID); projectErr == nil {
			taskProject = &project
		}
	}
	store.SetAuthTokenRefresher(func(refreshCtx context.Context, serverName string) (string, error) {
		account, ok := githubAccountsByServerName[serverName]
		if !ok {
			return "", fmt.Errorf("no renewable GitHub credential for %q", serverName)
		}
		return githubapp.TokenForMCPAccount(refreshCtx, e.q, account, taskProject)
	})

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
			cgSummary := cgProxy.RegisterAll(ctx, registry)
			defer cgProxy.Close()
			e.logInfo(proxyLogger, fmt.Sprintf("Codegraph: %d project(s) available", len(cgServers)))
			e.logInfo(proxyLogger, cgSummary)
		}
	} else if cgErr != nil {
		e.logInfo(proxyLogger, fmt.Sprintf("Warning: failed to load codegraph servers: %v", cgErr))
	}

	// Apply tool filter from agent config (if set). An empty AllowedTools means all tools.
	if agentCfg != nil && len(agentCfg.AllowedTools) > 0 {
		registry = registry.Filter(agentCfg.AllowedTools)
	}

	// Append the authoritative tool listing so prose tool references in agent
	// prompt files can never promise a tool that isn't actually registered.
	systemPrompt += registry.PromptListing()

	// MCP listing token costs — set if any external MCP servers are active for this run.
	var listingCostTotal int
	var listingCostByServer map[string]int

	// Load MCP accounts enabled for this agent and register external servers in the store.
	if accounts, mcpErr := e.q.ListMCPAccountsForAgent(ctx, agent.ID); mcpErr == nil && len(accounts) > 0 {
		allServers, _ := e.q.ListMCPServers(ctx, 0, 0) // all companies/users — accounts are already agent-scoped
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
			synthetic.Name = fmt.Sprintf("%s/%s", srv.Name, acc.Name)
			if srv.IsGitHub() {
				// User OAuth credentials are never supplied to the GitHub MCP
				// process. Use a fresh, renewable installation token instead.
				token, tokenErr := githubapp.TokenForMCPAccount(ctx, e.q, acc, taskProject)
				if tokenErr != nil || token == "" {
					if tokenErr == nil {
						tokenErr = fmt.Errorf("empty installation token")
					}
					e.logInfo(proxyLogger, fmt.Sprintf("Warning: skipping GitHub MCP account %q: installation token failed: %v", acc.Name, tokenErr))
					continue
				}
				synthetic.AuthToken = token
				githubAccountsByServerName[synthetic.Name] = acc
			} else {
				// Other MCP integrations continue to use their encrypted account
				// credential directly.
				authToken, decErr := secrets.Default().Decrypt(acc.AuthTokenEncrypted)
				if decErr != nil {
					e.logInfo(proxyLogger, fmt.Sprintf("Warning: skipping MCP account %q: %v", acc.Name, decErr))
					continue
				}
				synthetic.AuthToken = authToken
			}
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
	// The display name comes from the agent CONFIG when set — delegated
	// sessions reuse the parent's DB agent row, and labeling every
	// sub-session with the parent's name ("CEO") made log forensics
	// unreliable.
	agentDisplayName := agent.Name
	if agentCfg != nil && agentCfg.Name != "" {
		agentDisplayName = agentCfg.Name
	}
	// Decrypt the provider key at the point of use. A locked owner surfaces as a
	// clear provider-auth failure downstream rather than a silent empty key.
	apiKey, keyErr := secrets.Default().Decrypt(provider.ApiKeyEncrypted)
	if keyErr != nil {
		e.logInfo(proxyLogger, fmt.Sprintf("Warning: could not decrypt provider key: %v", keyErr))
	}
	llmClient := aicli.NewClient(provider.BaseUrl, apiKey, model)
	if groupMode {
		// The group router picks the real provider+model per request. X-Run-ID
		// lets it write model_switch entries into this run's log; switches-only
		// keeps it from double-logging requests/responses (the agent loop
		// below already does that). The gateway token authenticates this run
		// to the (otherwise locked) local proxy — it is only ever sent to the
		// in-process gateway, never to an external provider.
		gatewayAuth.token = runtokens.Default().Issue(run.ID)
		defer runtokens.Default().Revoke(run.ID)
		if err := gatewayAuth.configure(llmClient, provider); err != nil {
			e.failRun(ctx, run.ID, err.Error())
			return "failed"
		}
	}
	agentCfgObj := aicli.Config{
		Client:                llmClient,
		Registry:              registry,
		Mode:                  agentMode,
		ProviderName:          provider.Name,
		AgentName:             agentDisplayName,
		TerminalTools:         []string{"finish_task"},
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

	// Seed the loop's history: a resumed run continues from its persisted
	// conversation (captured mid-turn by a prior pause — see below); a fresh
	// run starts from the system prompt + task-derived initial messages.
	seedHistory := aicli.BuildHistory(systemPrompt, initialMessages)
	if resumeRun != nil {
		if uErr := json.Unmarshal([]byte(resumeRun.PausedHistory), &seedHistory); uErr != nil {
			e.failRun(ctx, run.ID, fmt.Sprintf("failed to resume run: could not parse saved conversation: %v", uErr))
			return "failed"
		}
		e.logInfo(proxyLogger, fmt.Sprintf("Resuming interrupted run %d (%d saved messages)", run.ID, len(seedHistory)))
	}

	// Only root sessions ever pause: a session with an active delegation tree
	// (parent != nil, or a root session currently blocked inside a delegating
	// tool call) is left to run to completion rather than attempting to pause
	// mid-delegation — the parent/child coordination channels in
	// waitForSubtaskEvent cannot survive a process restart. Since the pause
	// check only fires between a session's OWN turns (never while blocked
	// inside a tool call), a root session currently waiting on a child is
	// naturally not interrupted by draining; it only gets a chance to pause
	// once that delegation returns and it starts its next turn.
	var pauseFn aicli.PauseRequested
	if parent == nil {
		pauseFn = e.draining.Load
	}

	_, resultHistory, agentErr := aiAgent.RunWithHistory(runCtx, seedHistory, pauseFn)

	if agentErr != nil && errors.Is(agentErr, aicli.ErrPaused) {
		historyJSON, mErr := json.Marshal(resultHistory)
		if mErr != nil {
			// Can't persist the conversation — pausing would silently lose it,
			// so fail the run instead of pretending it can be resumed.
			e.logError(proxyLogger, fmt.Sprintf("failed to serialize paused run history: %v — failing the run instead", mErr))
			e.failRun(ctx, run.ID, fmt.Sprintf("update pause failed: could not serialize conversation: %v", mErr))
			return "failed"
		}
		if pErr := e.q.PauseRun(context.Background(), run.ID, string(historyJSON)); pErr != nil {
			fmt.Printf("Warning: failed to persist paused run %d: %v\n", run.ID, pErr)
		}
		e.logInfo(proxyLogger, "Run paused for server update — will resume automatically after restart")
		e.hub.BroadcastEventForCompany(task.CompanyID, "run_ended", map[string]interface{}{"run_id": run.ID, "status": "interrupted"})
		paused = true
		return "interrupted"
	}

	status := runStatusCompleted
	var runErrMsg string

	if agentErr != nil {
		if runCtx.Err() == context.Canceled {
			e.logInfo(proxyLogger, "Run canceled by user")
			if proxyLogger != nil {
				proxyLogger.LogOutcome("canceled", "canceled", finishResult.Status, agentDisplayName, task.ID, "Run canceled by user")
			}
			e.q.UpdateRunLog(context.Background(), run.ID, "", "canceled")
			e.hub.BroadcastEventForCompany(task.CompanyID, "run_ended", map[string]interface{}{"run_id": run.ID, "status": "canceled"})
			e.notifyParentOfSubtaskCompletion(context.Background(), task, "canceled")
			return "canceled"
		}
		status = "failed"
		runErrMsg = agentErr.Error()
		e.logError(proxyLogger, fmt.Sprintf("Agent error: %v", agentErr))
	}

	// If finish_task was not called, force a follow-up turn. A plain-text
	// response is not a successful completion: the agent must execute the
	// terminal tool so that the task status and handoff are persisted.
	forcedFinish := false
	if agentErr == nil && !taskFinished {
		forcedFinish = true
		e.logInfo(proxyLogger, "finish_task not called. Sending follow-up to force it.")
		_, followErr := aiAgent.Run(runCtx, systemPrompt,
			"You must call finish_task before ending. Choose the appropriate status: 'done' if complete, 'in-review' if a human should review the result, 'blocked' if stuck, or 'refinement' if you need clarification. Provide a short one-sentence finish_status.")
		if followErr != nil {
			e.logError(proxyLogger, fmt.Sprintf("Follow-up failed: %v", followErr))
			status = "failed"
			runErrMsg = fmt.Sprintf("finish_task was not called and the forced follow-up failed: %v", followErr)
		} else if !taskFinished {
			status = "failed"
			runErrMsg = "agent ended without calling finish_task, including during the forced follow-up"
		}
	}

	// Git commit if there are changes.
	if gitProject && gitMgr != nil && status == runStatusCompleted && finishAllowsGit(finishResult) {
		committed := e.tryGitCommit(ctx, proxyLogger, gitMgr, workspacePath, task, agent, gatewayAuth)
		if committed && task.ProjectID != nil {
			e.publishTaskPR(ctx, proxyLogger, gitMgr, workspacePath, task, finishResult)
		}
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

	// Close the trajectory with an outcome entry: how the loop terminated
	// and the agent's own verdict. Written last so it's the final line of
	// the run's JSONL log.
	if proxyLogger != nil {
		endReason := "no_finish"
		summary := finishResult.FinishStatus
		switch {
		case agentErr != nil && errors.Is(agentErr, aicli.ErrMaxTurns):
			endReason = "max_turns"
			summary = agentErr.Error()
		case agentErr != nil:
			endReason = "error"
			summary = agentErr.Error()
		case taskFinished && forcedFinish:
			endReason = "finish_task_forced"
		case taskFinished:
			endReason = "finish_task"
		}
		proxyLogger.LogOutcome(status, endReason, finishResult.Status, agentDisplayName, task.ID, summary)
	}

	e.q.UpdateRunLog(ctx, run.ID, runErrMsg, status)

	e.broadcastForTask(ctx, run.TaskID, "run_ended", map[string]interface{}{"run_id": run.ID, "status": status})

	// Notify the parent task that this subtask has completed or failed.
	e.notifyParentOfSubtaskCompletion(ctx, task, status)

	return status
}

func (e *NativeEngine) publishTaskPR(ctx context.Context, logger *logging.ProxyLogger, gitMgr *git.GitManager, workspace string, task db.Task, finish tools.FinishTaskResult) {
	if task.ProjectID == nil {
		return
	}
	project, err := e.q.GetProject(ctx, *task.ProjectID)
	if err != nil || project.GitHubInstallationID == 0 {
		return
	}
	branch := taskGitBranch(task.ID)
	if err := gitMgr.PushWorktreeBranch(ctx, workspace, branch); err != nil {
		e.logInfo(logger, "GitHub push failed: "+err.Error())
		return
	}
	token, err := githubapp.TokenForProject(ctx, project)
	if err != nil {
		e.logInfo(logger, "GitHub installation token failed: "+err.Error())
		return
	}
	gh, err := githubapp.FromEnv()
	if err != nil {
		e.logInfo(logger, "GitHub App client failed: "+err.Error())
		return
	}
	repositorySlug, err := githubapp.RepositorySlug(project.RepositoryUrl)
	if err != nil {
		e.logInfo(logger, "GitHub repository URL is invalid: "+err.Error())
		return
	}
	if task.GitHubPRNumber != 0 {
		e.logInfo(logger, fmt.Sprintf("Updated existing PR #%d: %s", task.GitHubPRNumber, task.GitHubPRURL))
		return
	}
	owner := strings.SplitN(repositorySlug, "/", 2)[0]
	existing, found, err := gh.FindOpenPullRequestByHead(ctx, token, repositorySlug, owner+":"+branch)
	if err != nil {
		e.logInfo(logger, "GitHub PR lookup failed: "+err.Error())
		return
	}
	if found {
		task.GitHubBranch, task.GitHubPRNumber, task.GitHubPRURL = branch, existing.Number, existing.HTMLURL
		_, _ = e.q.UpdateTask(ctx, task)
		e.logInfo(logger, fmt.Sprintf("Reused existing PR #%d: %s", existing.Number, existing.HTMLURL))
		return
	}
	title, description := pullRequestContent(task, finish)
	baseBranch := task.EffectiveGitBaseBranch()
	number, prURL, err := gh.CreatePullRequest(ctx, token, repositorySlug, title, branch, baseBranch, description)
	if err != nil {
		e.logInfo(logger, "GitHub PR creation failed: "+err.Error())
		return
	}
	task.GitHubBranch, task.GitHubPRNumber, task.GitHubPRURL = branch, number, prURL
	_, _ = e.q.UpdateTask(ctx, task)
	e.logInfo(logger, fmt.Sprintf("Created draft PR #%d: %s", number, prURL))
}

func pullRequestContent(task db.Task, finish tools.FinishTaskResult) (string, string) {
	title := strings.TrimSpace(finish.PullRequestTitle)
	if title == "" {
		title = task.Title
	}
	description := strings.TrimSpace(finish.PullRequestDescription)
	if description == "" {
		description = strings.TrimSpace(finish.ResultDetails)
	}
	if description == "" {
		description = strings.TrimSpace(finish.FinishStatus)
	}
	if description == "" {
		description = strings.TrimSpace(task.Description)
	}
	return title, description
}

// buildInitialMessages assembles a session's conversation seed: the task
// description (plus attachment names) as the first user message, followed by
// past run results and human/agent comments interleaved chronologically.
// Delegated subtasks carry no raw user input — their description IS the
// refined description written by the task owner.
func (e *NativeEngine) buildInitialMessages(ctx context.Context, task db.Task, mode string) []aicli.Message {
	comments, _ := e.q.ListCommentsByTask(ctx, task.ID)
	attachments, _ := e.q.ListAttachmentsByTask(ctx, task.ID)
	pastRuns, _ := e.q.ListCompletedRunsByTask(ctx, task.ID)

	taskDesc := task.Description
	if taskDesc == "" {
		taskDesc = task.RefinedDescription
	}
	taskContent := fmt.Sprintf("Task: %s\nDescription: %s\nMode: %s", task.Title, taskDesc, mode)
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

	// Past completed runs as compact JSON agent messages (description only,
	// not the full explanation).
	for _, r := range pastRuns {
		ts := r.StartedAt
		if r.EndedAt != nil {
			ts = *r.EndedAt
		}
		msg := fmt.Sprintf(`{"run_id":%d,"completed_at":"%s","result":%q}`,
			r.ID, ts.Format(time.RFC3339), r.ResultDescription)
		timeline = append(timeline, timelineEntry{t: ts, role: "assistant", text: msg})
	}

	// Human comments and non-task_done agent comments; status changes and
	// artifact notifications get a readable rendering.
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

	messages := []aicli.Message{{Role: "user", Content: taskContent}}
	for _, entry := range timeline {
		messages = append(messages, aicli.Message{Role: entry.role, Content: entry.text})
	}
	return messages
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
	e.broadcastForTask(ctx, taskID, "comment_created", questionComment)
	e.broadcastForTask(ctx, taskID, "human_input_requested", map[string]interface{}{
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

// makeCreateSubtaskFunc returns the callback behind the create_subtask tool.
// It creates a child task, runs it as a nested session linked to the parent
// run, and blocks until the sub-agent either finishes (returning its result
// and artifacts) or asks the owner a question via ask_task_owner (returning
// the question, to be answered with answer_subtask_question).
func (e *NativeEngine) makeCreateSubtaskFunc(
	parentTask db.Task,
	parentAgent db.Agent,
	parentRun db.Run,
	parentLogger *logging.ProxyLogger,
	rootRunID, rootTaskID int32,
	workspacePath string,
	depth int,
	allowedAgents []string,
	pending *pendingSubtasks,
) func(callCtx context.Context, title, description, agentName string) (string, error) {
	return func(callCtx context.Context, title, description, agentName string) (string, error) {
		// Reject if another subtask of this parent is already running — that
		// includes a subtask paused on an unanswered ask_task_owner question.
		runningCount, err := e.q.CountRunningSubtasks(callCtx, parentTask.ID)
		if err != nil {
			return "", fmt.Errorf("failed to check running subtasks: %w", err)
		}
		if runningCount > 0 {
			return "", fmt.Errorf("a subtask of task %d is already running; wait for it to finish — if it asked a question, reply with answer_subtask_question first", parentTask.ID)
		}

		if e.agentFactory == nil {
			return "", fmt.Errorf("no agent configs available for delegation")
		}
		if _, cfgErr := e.agentFactory.GetConfig(agentName); cfgErr != nil {
			return "", fmt.Errorf("unknown agent config %q: %w", agentName, cfgErr)
		}
		allowed := len(allowedAgents) == 0
		for _, a := range allowedAgents {
			if a == agentName {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("agent %q is not one of your sub-agents (%s)", agentName, strings.Join(allowedAgents, ", "))
		}

		parentID := parentTask.ID
		agentID := parentAgent.ID
		subtask, err := e.q.CreateTask(callCtx, db.Task{
			CompanyID: parentTask.CompanyID,
			ProjectID: parentTask.ProjectID,
			SprintID:  parentTask.SprintID,
			AgentID:   &agentID,
			ParentID:  &parentID,
			Title:     title,
			// Delegated subtasks have no raw user input: the owner's
			// instructions ARE the refined description.
			RefinedDescription: description,
			TaskType:           db.TaskTypeImplement,
			Status:             "in-progress",
			Priority:           "Normal",
			AgentConfigName:    agentName,
		})
		if err != nil {
			return "", fmt.Errorf("failed to create subtask: %w", err)
		}

		e.hub.BroadcastEventForCompany(subtask.CompanyID, "task_created", map[string]interface{}{
			"id":        subtask.ID,
			"parent_id": parentTask.ID,
			"title":     subtask.Title,
		})

		state := &delegationState{
			subtaskID: subtask.ID,
			agentName: agentName,
			title:     title,
			eventCh:   make(chan subtaskEvent),
			answerCh:  make(chan string),
		}

		session := &parentSession{
			parentRunID:   parentRun.ID,
			rootRunID:     rootRunID,
			rootTaskID:    rootTaskID,
			workspacePath: workspacePath,
			depth:         depth + 1,
			onRunCreated: func(childRun db.Run) {
				state.childRunID = childRun.ID
				if parentLogger != nil {
					parentLogger.LogSessionStarted(childRun.ID, subtask.ID, agentName, title, fmt.Sprintf("session-%d.jsonl", childRun.ID))
				}
				e.hub.BroadcastEventForCompany(subtask.CompanyID, "session_started", map[string]interface{}{
					"parent_run_id": parentRun.ID,
					"run_id":        childRun.ID,
					"task_id":       subtask.ID,
					"agent_name":    agentName,
					"title":         title,
				})
			},
			askOwner: func(qCtx context.Context, question string) (string, error) {
				e.recordSubtaskQA(qCtx, subtask.ID, state.childRunID, "ask_owner", question)
				select {
				case state.eventCh <- subtaskEvent{question: question}:
				case <-qCtx.Done():
					return "", qCtx.Err()
				}
				select {
				case answer := <-state.answerCh:
					return answer, nil
				case <-qCtx.Done():
					return "", qCtx.Err()
				}
			},
		}

		// Run the child session in a goroutine so this call can also react to
		// ask_task_owner questions while the session is still in flight.
		go func() {
			status := e.executeSession(callCtx, subtask, "implement", session, nil)
			select {
			case state.eventCh <- subtaskEvent{status: status, done: true}:
			case <-callCtx.Done():
			}
		}()

		return e.waitForSubtaskEvent(callCtx, state, rootTaskID, pending, parentLogger, parentRun)
	}
}

// waitForSubtaskEvent blocks until the subtask session either asks the owner
// a question (recorded in pending, question returned as the tool result) or
// finishes (final result returned).
func (e *NativeEngine) waitForSubtaskEvent(
	ctx context.Context,
	state *delegationState,
	rootTaskID int32,
	pending *pendingSubtasks,
	logger *logging.ProxyLogger,
	ownerRun db.Run,
) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case ev := <-state.eventCh:
		if !ev.done {
			pending.put(state)
			e.logInfo(logger, fmt.Sprintf("Subtask #%d asked its owner: %s", state.subtaskID, ev.question))
			e.broadcastForTask(ctx, state.subtaskID, "subtask_question", map[string]interface{}{
				"subtask_id":   state.subtaskID,
				"owner_run_id": ownerRun.ID,
				"question":     ev.question,
			})
			return fmt.Sprintf("Subtask #%d (%s, %q) asks you a question and is paused until you reply:\n\n%s\n\n"+
				"Reply with answer_subtask_question (subtask_id=%d). The subtask keeps its full context and resumes with your answer.",
				state.subtaskID, state.agentName, state.title, ev.question, state.subtaskID), nil
		}
		return e.buildSubtaskReply(state, ev.status, rootTaskID, logger, ownerRun)
	}
}

// buildSubtaskReply assembles the create_subtask / answer_subtask_question
// tool result for a finished subtask session: final status, result summary,
// detailed handoff, and the artifacts the session produced.
func (e *NativeEngine) buildSubtaskReply(
	state *delegationState,
	status string,
	rootTaskID int32,
	logger *logging.ProxyLogger,
	ownerRun db.Run,
) (string, error) {
	result := ""
	var childErr, childRunName, childDetails, childArtifacts string
	if state.childRunID > 0 {
		if childRun, runErr := e.q.GetRun(context.Background(), state.childRunID); runErr == nil {
			result = childRun.ResultDescription
			childRunName = childRun.Name
			if childRun.ResultExplanation != "" && childRun.ResultExplanation != childRun.ResultDescription {
				childDetails = "\nDetails:\n" + childRun.ResultExplanation
			}
			if childRun.Status == "failed" {
				childErr = childRun.LogContent
			}
		}
		if arts, aErr := e.q.ListArtifactsByTaskTree(context.Background(), rootTaskID); aErr == nil {
			var written []string
			for _, a := range arts {
				if a.RunID == state.childRunID {
					written = append(written, fmt.Sprintf("%q", a.Filename))
				}
			}
			if len(written) > 0 {
				childArtifacts = fmt.Sprintf("\nArtifacts written by this subtask: %s — read them with read_artifact.", strings.Join(written, ", "))
			}
		}
	}
	if logger != nil {
		logger.LogSessionEnded(state.childRunID, status, result)
	}
	e.broadcastForTask(context.Background(), ownerRun.TaskID, "session_ended", map[string]interface{}{
		"parent_run_id": ownerRun.ID,
		"run_id":        state.childRunID,
		"status":        status,
	})

	taskStatus := ""
	if finalTask, taskErr := e.q.GetTask(context.Background(), state.subtaskID); taskErr == nil {
		taskStatus = finalTask.Status
	}
	if result == "" {
		result = "(no result summary provided)"
	}
	runLabel := fmt.Sprintf("run #%d", state.childRunID)
	if childRunName != "" {
		runLabel = fmt.Sprintf("%s (run #%d)", childRunName, state.childRunID)
	}
	reply := fmt.Sprintf("Subtask #%d (%s) finished.\nSession: %s, status %s\nSubtask status: %s\nResult: %s",
		state.subtaskID, state.agentName, runLabel, status, taskStatus, result)
	reply += childDetails + childArtifacts
	if status != runStatusCompleted {
		if childErr != "" {
			reply += "\nError: " + childErr
		}
		reply += "\nThe subtask session did not complete successfully. Decide how to proceed: retry with better instructions, assign a different sub-agent, or escalate via ask_human."
	}
	return reply, nil
}

// createBoardTask is the callback behind the create_task tool: it creates a
// new top-level task on the board (mirroring the API's CreateTask endpoint)
// and, when created in "to-do", kicks off its execution as an independent
// root run — exactly as if a human had moved the card there.
func (e *NativeEngine) createBoardTask(ctx context.Context, creator db.Task, agentID int32, company db.Company, p tools.CreateTaskParams) (string, error) {
	status := p.Status
	if status == "" {
		status = "backlog"
	}
	if status != "backlog" && status != "to-do" {
		return "", fmt.Errorf("status must be \"backlog\" or \"to-do\", got %q", status)
	}
	priority := p.Priority
	if priority == "" {
		priority = "Normal"
	}
	taskType := p.TaskType
	if taskType == "" {
		taskType = db.TaskTypePlanAndImplement
	}
	if p.AgentConfigName != "" && e.agentFactory != nil {
		if _, err := e.agentFactory.GetConfig(p.AgentConfigName); err != nil {
			return "", fmt.Errorf("unknown agent config %q", p.AgentConfigName)
		}
	}

	sprintID := creator.SprintID
	if p.SprintID != 0 {
		sprintID = p.SprintID
	}
	projectID := creator.ProjectID
	if p.ProjectID != 0 {
		project, err := e.q.GetProject(ctx, p.ProjectID)
		if err != nil {
			return "", fmt.Errorf("project %d not found", p.ProjectID)
		}
		if project.CompanyID != company.ID {
			return "", fmt.Errorf("project %d belongs to another company", p.ProjectID)
		}
		pid := project.ID
		projectID = &pid
	}
	var dueDate *time.Time
	if p.DueDate != "" {
		t, err := time.Parse(time.RFC3339, p.DueDate)
		if err != nil {
			return "", fmt.Errorf("invalid due_date %q — use RFC3339, e.g. 2026-08-01T00:00:00Z", p.DueDate)
		}
		dueDate = &t
	}

	newTask, err := e.q.CreateTask(ctx, db.Task{
		CompanyID:       company.ID,
		ProjectID:       projectID,
		SprintID:        sprintID,
		AgentID:         &agentID,
		Title:           p.Title,
		Description:     p.Description,
		TaskType:        taskType,
		Status:          status,
		Priority:        priority,
		DueDate:         dueDate,
		AgentConfigName: p.AgentConfigName,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create task: %w", err)
	}
	e.hub.BroadcastEventForCompany(newTask.CompanyID, "task_created", newTask)

	if status == "to-do" {
		// Independent root run, same as a human moving the card to "to-do".
		newTaskID := newTask.ID
		go func() {
			if perr := e.ProcessTask(context.Background(), newTaskID); perr != nil {
				fmt.Printf("Warning: failed to start created task %d: %v\n", newTaskID, perr)
			}
		}()
	}

	ref := newTask.RefKey
	if ref == "" {
		ref = fmt.Sprintf("#%d", newTask.ID)
	}
	reply := fmt.Sprintf("Task %s created on the board: %q (status %s, priority %s).", ref, newTask.Title, status, priority)
	if status == "to-do" {
		reply += " It starts executing independently of this session."
	}
	return reply, nil
}

// ownerUserIDForCompany resolves a company's owning user, or 0 when unset —
// Default Models settings are per-user, so internal one-shot LLM calls made
// on behalf of a task use the task owner's configuration.
func (e *NativeEngine) ownerUserIDForCompany(ctx context.Context, companyID int32) int32 {
	company, err := e.q.GetCompany(ctx, companyID)
	if err != nil || company.UserID == nil {
		return 0
	}
	return *company.UserID
}

func (e *NativeEngine) ownerUserIDForCompanyOfTask(ctx context.Context, taskID int32) int32 {
	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return 0
	}
	return e.ownerUserIDForCompany(ctx, task.CompanyID)
}

// resolvePurposeModel resolves the provider/model pair configured for one
// internal-use purpose (see db.PurposeCommitMessages, db.PurposeAskArtifact)
// via the "Default Models" settings — independent of any Model Group's own
// definition. A purpose can point at a fixed provider+model or at any model
// group (in which case its free members are tried first, tie-broken by
// priority — the same ordering the group gateway uses, minus live health
// tracking, which isn't worth the bookkeeping for an infrequent one-shot
// call). Falls back to the session's own provider and model when the
// purpose has no override configured, or its target no longer resolves.
func (e *NativeEngine) resolvePurposeModel(ctx context.Context, userID int32, purpose string, sessionProvider db.LLMProvider, sessionModel string) (db.LLMProvider, string) {
	setting, err := e.q.GetDefaultModelSetting(ctx, userID, purpose)
	if err != nil {
		return sessionProvider, sessionModel
	}

	if setting.ModelGroupID != nil {
		group, gErr := e.q.GetModelGroup(ctx, *setting.ModelGroupID)
		if gErr != nil {
			return sessionProvider, sessionModel
		}
		members := db.ExpandModelGroupMembers(group.Members)
		if len(members) == 0 {
			return sessionProvider, sessionModel
		}
		sort.SliceStable(members, func(i, j int) bool {
			if members[i].IsFree != members[j].IsFree {
				return members[i].IsFree
			}
			return members[i].Priority < members[j].Priority
		})
		best := members[0]
		return best.Provider, best.Model
	}

	if setting.ProviderID != nil {
		provider, pErr := e.q.GetLLMProvider(ctx, *setting.ProviderID)
		if pErr != nil {
			return sessionProvider, sessionModel
		}
		model := setting.Model
		if model == "" {
			model = provider.DefaultModel
		}
		return provider, model
	}

	return sessionProvider, sessionModel
}

// askArtifact answers a question about one artifact via a separate one-shot
// LLM call, so the artifact's content never enters the asking agent's
// context — only the short answer is returned as the tool result. It uses the
// configured "ask_artifact" Default Model when set, falling back to the asking
// session's model, and bills the reader call's tokens to the asking run.
// Each reader call is logged to its own file in the run's log folder.
func (e *NativeEngine) askArtifact(
	ctx context.Context,
	runID, rootTaskID int32,
	provider db.LLMProvider,
	sessionModel string,
	gatewayAuth runGatewayAuth,
	filename, question string,
	logger *logging.ProxyLogger,
) (string, error) {
	arts, err := e.q.ListArtifactsByTaskTree(ctx, rootTaskID)
	if err != nil {
		return "", err
	}
	content, found := "", false
	for i := len(arts) - 1; i >= 0; i-- {
		if arts[i].Filename == filename {
			content = arts[i].Content
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("artifact %q not found — call list_artifacts to see what exists", filename)
	}

	const maxArtifactChars = 100000
	truncNote := ""
	if len(content) > maxArtifactChars {
		content = content[:maxArtifactChars]
		truncNote = "\n\n[Document truncated for length — the answer is based on the first part only.]"
	}

	provider, model := e.resolvePurposeModel(ctx, e.ownerUserIDForCompanyOfTask(ctx, rootTaskID), db.PurposeAskArtifact, provider, sessionModel)

	prompt := fmt.Sprintf(`You answer questions about a document. Answer concisely — a short, direct answer (a few sentences at most), quoting brief evidence from the document when helpful. Base the answer ONLY on the document; if the document does not contain the answer, say so plainly.

Document %q:
%s%s

Question: %s`, filename, content, truncNote, question)

	apiKey := ""
	if provider.ApiKeyEncrypted != "" {
		apiKey, err = secrets.Default().Decrypt(provider.ApiKeyEncrypted)
		if err != nil {
			return "", fmt.Errorf("artifact reader: decrypt provider key: %w", err)
		}
	}
	client := aicli.NewClient(provider.BaseUrl, apiKey, model)
	if err := gatewayAuth.configure(client, provider); err != nil {
		return "", fmt.Errorf("artifact reader: %w", err)
	}
	resp, _, err := client.Complete(ctx, aicli.ChatRequest{
		Messages:  []aicli.Message{{Role: "user", Content: prompt}},
		MaxTokens: 500,
	})
	if err != nil {
		return "", fmt.Errorf("artifact reader call failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("artifact reader returned no answer")
	}
	// Bill the reader call to the asking session's run.
	if runID > 0 {
		if statErr := e.q.AddRunTokenStats(context.Background(), runID, db.RunTokenStats{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
		}); statErr != nil {
			fmt.Printf("Warning: failed to record ask_artifact token stats: %v\n", statErr)
		}
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		return "", fmt.Errorf("artifact reader returned an empty answer")
	}
	e.logAskArtifact(logger, runID, filename, model, question, prompt, answer, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	return fmt.Sprintf("Answer about %q: %s", filename, answer), nil
}

// isModelGroupProxyBaseURL identifies the synthetic provider target used for
// native runs whose agent is bound to a model group. It is path-based so local
// test servers and non-default ports work too.
func isModelGroupProxyBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && strings.HasPrefix(u.Path, "/api/proxy/group/")
}

type runGatewayAuth struct {
	runID int32
	token string
}

func (a runGatewayAuth) configure(client *aicli.Client, provider db.LLMProvider) error {
	if !isModelGroupProxyBaseURL(provider.BaseUrl) {
		return nil
	}
	if a.token == "" {
		return fmt.Errorf("model-group gateway token is unavailable")
	}
	client.ExtraHeaders = modelGroupGatewayHeaders(a.runID, a.token)
	return nil
}

func modelGroupGatewayHeaders(runID int32, token string) map[string]string {
	return map[string]string{
		runtokens.TokenHeader: token,
		"X-Run-ID":            fmt.Sprintf("%d", runID),
		"X-Proxy-Log-Mode":    "switches-only",
	}
}

// logAskArtifact persists one ask_artifact reader exchange to its own file in
// the run's log folder (alongside main.jsonl / session-N.jsonl), so the short
// answer in the session log can always be traced back to the full reader
// prompt and the exact artifact content it saw.
func (e *NativeEngine) logAskArtifact(logger *logging.ProxyLogger, runID int32, filename, model, question, prompt, answer string, promptTokens, completionTokens int) {
	if logger == nil {
		return
	}
	logName := fmt.Sprintf("ask-artifact-%d-%d.log", runID, time.Now().UnixMilli())
	logPath := filepath.Join(filepath.Dir(logger.FilePath()), logName)
	content := fmt.Sprintf(`=== ask_artifact ===
Time: %s
Asking run: #%d
Artifact: %s
Reader model: %s
Question: %s
Usage: prompt_tokens=%d completion_tokens=%d

--- Reader prompt (artifact content as sent) ---
%s

--- Answer ---
%s
`, time.Now().UTC().Format(time.RFC3339), runID, filename, model, question, promptTokens, completionTokens, prompt, answer)
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to write ask_artifact log: %v", err))
		return
	}
	e.logInfo(logger, fmt.Sprintf("ask_artifact %q (model %s) — full exchange in %s", filename, model, logName))
}

// recordSubtaskQA stores an ask_task_owner question or the owner's answer as
// a comment on the subtask, so the exchange is visible in the task activity.
func (e *NativeEngine) recordSubtaskQA(ctx context.Context, taskID, runID int32, commentType, content string) {
	comment := db.Comment{
		TaskID:      taskID,
		AuthorType:  "agent",
		CommentType: commentType,
		Content:     content,
	}
	if runID > 0 {
		rid := runID
		comment.RunID = &rid
	}
	created, err := e.q.CreateComment(ctx, comment)
	if err != nil {
		fmt.Printf("Warning: failed to record subtask %s comment: %v\n", commentType, err)
		return
	}
	e.broadcastForTask(ctx, taskID, "comment_created", created)
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
	e.broadcastForTask(ctx, *subtask.ParentID, "comment_created", comment)
	e.broadcastForTask(ctx, *subtask.ParentID, "subtask_completed", map[string]interface{}{
		"subtask_id":    subtask.ID,
		"parent_id":     *subtask.ParentID,
		"status":        status,
		"subtask_title": subtask.Title,
	})
}

// formatArtifactList renders artifact metadata (never content) for agent
// system prompts: name, size, line count, modify time, verified flag and the
// producer's one-line description.
func formatArtifactList(arts []db.Artifact) string {
	var b strings.Builder
	for _, a := range arts {
		lines := strings.Count(a.Content, "\n") + 1
		verified := "no"
		if a.IsVerified {
			verified = "yes"
		}
		fmt.Fprintf(&b, "- %s (%d bytes, %d lines, modified %s, verified: %s)",
			a.Filename, len(a.Content), lines, a.UpdatedAt.Format("2006-01-02 15:04"), verified)
		if a.Description != "" {
			fmt.Fprintf(&b, " — %s", a.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// broadcastForTask delivers a tenant-scoped event by resolving the task's
// company owner; when the task can't be loaded the event goes to in-process
// subscribers only (fail closed for WS clients).
func (e *NativeEngine) broadcastForTask(ctx context.Context, taskID int32, event string, payload interface{}) {
	if task, err := e.q.GetTask(ctx, taskID); err == nil {
		e.hub.BroadcastEventForCompany(task.CompanyID, event, payload)
		return
	}
	e.hub.BroadcastEventForCompany(-1, event, payload)
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
		e.broadcastForTask(ctx, taskID, "comment_created", comment)
	}
}

// failRun marks a run as failed and broadcasts the event.
func (e *NativeEngine) failRun(ctx context.Context, runID int32, errMsg string) {
	e.q.UpdateRunLog(ctx, runID, errMsg, "failed")
	payload := map[string]interface{}{"run_id": runID, "status": "failed"}
	if run, err := e.q.GetRun(ctx, runID); err == nil {
		e.broadcastForTask(ctx, run.TaskID, "run_ended", payload)
	} else {
		e.hub.BroadcastEventForCompany(-1, "run_ended", payload) // owner unknown — subscribers only
	}
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
func (e *NativeEngine) tryGitCommit(ctx context.Context, logger *logging.ProxyLogger, gitMgr *git.GitManager, workspacePath string, task db.Task, agent db.Agent, gatewayAuth runGatewayAuth) bool {
	// Skip cleanly when the workspace is not a git worktree (e.g. worktree
	// creation failed or the task has a bare directory workspace) instead of
	// letting `git diff` fail with usage noise.
	if _, statErr := os.Stat(filepath.Join(workspacePath, ".git")); statErr != nil {
		e.logInfo(logger, "Workspace is not a git worktree; skipping commit")
		return false
	}
	e.logInfo(logger, "Checking for changes to commit in worktree...")
	status, err := gitMgr.GetStatusInDir(ctx, workspacePath)
	if err != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to get worktree status: %v", err))
		return false
	}
	status = commitRelevantGitStatus(status)
	if strings.TrimSpace(status) == "" {
		e.logInfo(logger, "No changes to commit")
		return false
	}
	diff, err := gitMgr.GetDiffInDir(ctx, workspacePath)
	if err != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to get diff: %v", err))
		return false
	}
	// git diff omits untracked and staged-only changes. Status proves that a
	// commit is needed and still gives the message generator useful context.
	if strings.TrimSpace(diff) == "" {
		diff = status
	}

	// Generate commit message via LLM.
	commitMsg, msgErr := e.generateCommitMessage(ctx, agent, diff, task, gatewayAuth)
	if msgErr != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to generate commit message: %v, using fallback", msgErr))
		commitMsg = fmt.Sprintf("Agent run for task %d", task.ID)
	}

	if commitErr := gitMgr.CommitInWorktree(ctx, workspacePath, commitMsg); commitErr != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to commit: %v", commitErr))
		return false
	} else {
		e.logInfo(logger, fmt.Sprintf("Committed changes: %s", commitMsg))
		return true
	}
}

// finishAllowsGit limits repository publication to successful agent verdicts.
// A blocked/refinement result may have touched the worktree while preparing a
// handoff, but it must not be turned into an automatic commit or PR.
func finishAllowsGit(result tools.FinishTaskResult) bool {
	return result.Status == "done" || result.Status == "in-review"
}

// commitRelevantGitStatus removes the per-task memory file from publication.
// It is created automatically in every task worktree and is execution
// metadata, not a project change.
func commitRelevantGitStatus(status string) string {
	var relevant []string
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) >= 3 && strings.TrimSpace(line[3:]) == "memory.md" {
			continue
		}
		relevant = append(relevant, line)
	}
	return strings.Join(relevant, "\n")
}

// generateCommitMessage calls the LLM to summarise a diff into a commit message.
func (e *NativeEngine) generateCommitMessage(ctx context.Context, agent db.Agent, diff string, task db.Task, gatewayAuth runGatewayAuth) (string, error) {
	if agent.ProviderID == nil {
		return "", fmt.Errorf("no provider configured")
	}
	provider, err := e.q.GetLLMProvider(ctx, *agent.ProviderID)
	if err != nil {
		return "", err
	}
	// Commit messages are a lightweight internal task: prefer the configured
	// "Default Models" target for it, when one is set.
	sessionModel := agent.Model
	if sessionModel == "" {
		sessionModel = provider.DefaultModel
	}
	provider, model := e.resolvePurposeModel(ctx, e.ownerUserIDForCompany(ctx, task.CompanyID), db.PurposeCommitMessages, provider, sessionModel)
	const maxDiffChars = 60000
	if len(diff) > maxDiffChars {
		diff = diff[:maxDiffChars] + "\n... (truncated)"
	}
	prompt := fmt.Sprintf(`Summarize these code changes into a concise git commit message.
Subject line max 72 chars. Optional body separated by blank line.
Respond with ONLY the commit message, no quotes or explanation.

Task: %s
Changes:
%s`, task.Title, diff)

	apiKey, err := secrets.Default().Decrypt(provider.ApiKeyEncrypted)
	if err != nil {
		return "", fmt.Errorf("commit message: decrypt provider key: %w", err)
	}
	client := aicli.NewClient(provider.BaseUrl, apiKey, model)
	if err := gatewayAuth.configure(client, provider); err != nil {
		return "", fmt.Errorf("commit message: %w", err)
	}
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
