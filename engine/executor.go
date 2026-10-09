package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/engine/workflow"
	"agent-orchestrator/pkg/filesystem"
	gitpkg "agent-orchestrator/pkg/git"
	"agent-orchestrator/pkg/githubapp"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"
)

const (
	// maxConcurrentExecutors bounds how many executor sessions run at once.
	maxConcurrentExecutors = 6
	// executorHeartbeat is how often a live session marks itself alive, and
	// executorStaleAfter how long without that mark before it is taken for
	// dead.
	executorHeartbeat  = 20 * time.Second
	executorStaleAfter = 2 * time.Minute
	// maxFinishReminders is how often a session that stopped talking without
	// reporting is told to finish before it is given up on.
	maxFinishReminders = 2

	// resumeAfterRestart is the recorded reason of a session paused for, and
	// resumed after, a planned restart.
	resumeAfterRestart = "binary_update"

	sessionStoppedResponding = "the executor session stopped responding"
	sessionEndedSilently     = "the executor session ended without calling finish_work"
)

// executorSession is one executor at work on one direct task: a chat loop on
// the cheap tier with the full tool set, ending in a structured report.
//
// The session writes only its own run row — the report, then its final
// status. It never touches the task's workflow state: when it ends it asks
// the driver to look at the task, and the task's own step applies the stored
// report. So a session can die at any point without leaving the task between
// states.
type executorSession struct {
	e       *NativeEngine
	task    db.Task
	root    db.Task
	run     db.Run
	agent   db.Agent
	company db.Company
	target  modelTarget

	logger    *logging.ProxyLogger
	workspace string
	readOnly  []string
	// attachments are the files the human attached to the task's tree.
	attachments []string
	artifactDir string
	writer      bool
	git         *gitpkg.GitManager
	cleanups    []func()

	checkpointTool *tools.Checkpoint
	checkpoints    checkpointTracker
	reminders      int
	report         *workflow.Report

	// gate is the company's classifier, or nil. turn is what it has made of
	// the session so far.
	gate *gate
	turn struct {
		// judged is the number of tool calls in the conversation when the
		// classifier last looked at it.
		judged int
		// notable: something worth recording happened since the last checkpoint.
		notable bool
		// loopHits counts turns in a row that looked like going in circles;
		// stopping is set once the session has been told to stop and report.
		loopHits int
		stopping bool
	}
}

func (s *executorSession) close() {
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		s.cleanups[i]()
	}
}

// reserveExecutorSlot takes one of the executor slots if any is free.
func (e *NativeEngine) reserveExecutorSlot() bool {
	if e.runs.draining.Load() {
		// Shutting down: the task stays ready and starts after the restart.
		return false
	}
	for {
		used := e.executorSlots.Load()
		if used >= maxConcurrentExecutors {
			return false
		}
		if e.executorSlots.CompareAndSwap(used, used+1) {
			return true
		}
	}
}

func (e *NativeEngine) releaseExecutorSlot() { e.executorSlots.Add(-1) }

// launchExecutor starts the session for a run the driver just created. The
// driver reserved its slot; the session gives it back when it ends.
func (e *NativeEngine) launchExecutor(_ db.Task, run db.Run) {
	e.runs.active.Add(1)
	go func() {
		defer e.runs.active.Done()
		defer e.releaseExecutorSlot()
		e.runExecutor(run.ID, false)
	}()
}

// runExecutor runs one executor session to its end and records how it ended.
func (e *NativeEngine) runExecutor(runID int32, resumed bool) {
	background := context.Background()
	run, task, err := e.q.GetRunWithTask(background, runID)
	if err != nil {
		fmt.Printf("Warning: executor could not load run %d: %v\n", runID, err)
		return
	}
	// Whatever happens below, the task is looked at when the session is over.
	defer e.driver.Enqueue(task.ID)

	ctx, cancel := context.WithCancel(background)
	e.runs.cancelFuncs.Store(run.ID, cancel)
	defer func() {
		cancel()
		e.runs.cancelFuncs.Delete(run.ID)
	}()
	// Heartbeat independently of the conversation: one model call or one
	// tool can legitimately take minutes.
	_ = e.q.TouchRunLastMessageTime(background, run.ID)
	go func() {
		ticker := time.NewTicker(executorHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = e.q.TouchRunLastMessageTime(background, run.ID)
			}
		}
	}()

	e.hub.BroadcastEventForCompany(task.CompanyID, "run_started", run)
	session := &executorSession{e: e, task: task, run: run}
	status, message := session.execute(ctx, resumed)
	session.close()

	if status == db.RunStatusPaused {
		e.hub.BroadcastEventForCompany(task.CompanyID, "run_paused", map[string]interface{}{"run_id": run.ID, "status": status})
		return
	}
	if err := e.q.UpdateRunLog(background, run.ID, message, status); err != nil {
		fmt.Printf("Warning: executor could not record the end of run %d: %v\n", run.ID, err)
	}
	if resumed {
		_ = e.q.ClearRunCheckpoint(background, run.ID)
	}
	e.hub.BroadcastEventForCompany(task.CompanyID, "run_ended", map[string]interface{}{"run_id": run.ID, "status": status})
}

// execute runs the session and returns the status its run ends in, with the
// reason when that is not success.
func (s *executorSession) execute(ctx context.Context, resumed bool) (status, message string) {
	if err := s.prepare(ctx); err != nil {
		return "failed", err.Error()
	}
	registry := s.buildTools()
	system, user := s.prompts(ctx)
	integrations := s.e.configureSessionIntegrations(ctx, s.task, registry, system, s.logger)
	defer integrations.close()

	apiKey, err := secrets.Default().Decrypt(s.target.Provider.ApiKeyEncrypted)
	if errors.Is(err, secrets.ErrLocked) {
		return "failed", vaultLockedDetail
	}
	if err != nil {
		return "failed", "could not read the model's API key: " + err.Error()
	}
	client := s.e.driver.newClient(s.target.Provider.BaseUrl, apiKey, s.target.Model)
	client.SessionID = runSession(s.run)
	if s.target.viaGateway() {
		token := runtokens.Default().Issue(s.run.ID)
		defer runtokens.Default().Revoke(s.run.ID)
		client.ExtraHeaders = modelGroupGatewayHeaders(s.run.ID, token)
	}

	config := aicli.Config{
		Client:                      client,
		Registry:                    integrations.registry,
		ProviderName:                s.target.Provider.Name,
		AgentName:                   s.agent.Name,
		TerminalTools:               []string{string(aicli.ToolFinishWork)},
		MaxTurns:                    s.e.driver.budgets.MaxExecutorTurns,
		MCPListingCostPerTurn:       integrations.listingCostTotal,
		MCPServerListingCosts:       integrations.listingCostByServer,
		Queries:                     s.e.q,
		RunID:                       s.run.ID,
		Logger:                      s.logger,
		InitialConversationSequence: s.run.Recovery.CheckpointSequence,
		HistoryAlreadyLogged:        resumed,
		BeforeTurn:                  s.beforeTurn,
		ToolsForTurn:                s.toolsForTurn,
		OnModelCall:                 s.onModelCall,
		OnFinalText:                 s.onFinalText,
	}
	if s.logger == nil {
		config.Logger = nil
	}
	history := aicli.BuildHistory(integrations.systemPrompt, []aicli.Message{{Role: "user", Content: user}})
	if resumed {
		config.ResumeNotice = "This session was interrupted by a server restart and has been resumed. Continue the task from where the conversation left off; do not repeat completed work."
		loaded, err := aicli.LoadMessageHistory(s.run.LogFilePath, s.run.Recovery.CheckpointSequence)
		if err != nil || len(loaded) == 0 {
			return "failed", fmt.Sprintf("could not restore the interrupted session: %v", err)
		}
		history = loaded
	}
	s.checkpoints = checkpointTracker{every: s.e.driver.budgets.CheckpointEveryToolCalls}
	s.checkpoints.start(history)
	s.gate = classifierFor(ctx, s.e.q, s.company, runSession(s.run))
	s.turn.judged, _ = toolCalls(history)
	s.info(fmt.Sprintf("Executor for task %s (attempt %d, model %s via %s) in %s", s.task.RefKey, s.run.Attempt, s.target.Model, s.target.Provider.Name, s.workspace))

	agent := aicli.New(config)
	_, _, runErr := agent.RunWithHistory(ctx, history, func() bool { return s.e.runs.draining.Load() })

	switch {
	case errors.Is(runErr, aicli.ErrPaused):
		return s.pause(agent)
	case ctx.Err() != nil:
		s.outcome("canceled", "stopped", "")
		return "canceled", "stopped"
	case s.report == nil && errors.Is(runErr, aicli.ErrModelCall):
		detail := modelFailureDetail(runErr)
		s.outcome("failed", "model_error", detail)
		return "failed", detail
	case s.report == nil && runErr != nil:
		s.outcome("failed", "error", runErr.Error())
		return "failed", runErr.Error()
	case s.report == nil:
		s.outcome("failed", "no_report", sessionEndedSilently)
		return "failed", sessionEndedSilently
	}
	if s.writer && s.git != nil && s.report.Status == workflow.ReportDone {
		s.commit(ctx)
	}
	s.outcome("completed", string(aicli.ToolFinishWork), s.report.Summary)
	return "completed", ""
}

// prepare loads what the session needs and sets up where it works.
func (s *executorSession) prepare(ctx context.Context) error {
	q := s.e.q
	var err error
	if s.agent, err = q.GetAgent(ctx, s.run.AgentID); err != nil {
		return fmt.Errorf("the agent this task runs as no longer exists: %w", err)
	}
	if s.company, err = q.GetCompany(ctx, s.task.CompanyID); err != nil {
		return err
	}
	if s.root, err = q.GetRootTask(ctx, s.task.ID); err != nil {
		return err
	}
	var status workflow.ModelStatus
	var detail string
	if s.target, status, detail = modelStatus(ctx, q, s.task); status != workflow.ModelReady {
		return errors.New(detail)
	}

	settings := loadSettings()
	manager := filesystem.NewManager(settings.BasePath)
	paths := manager.Paths()
	short := s.company.ShortName
	worktree := manager.GetTaskWorktreePath(s.company, s.root)
	s.artifactDir = paths.TaskArtifactsDir(short, s.root.ID)
	s.writer = workflow.WritesWorkspace(s.task.TaskType)

	if logger, err := logging.NewTaskRunLogger(settings.BasePath, short, s.root.ID, s.task.ID, s.run.ID, s.e.hub.ForCompany(s.task.CompanyID), q); err != nil {
		fmt.Printf("Warning: executor could not open the log of run %d: %v\n", s.run.ID, err)
	} else {
		s.logger = logger
		s.cleanups = append(s.cleanups, func() { _ = logger.Close() })
		s.run.LogFilePath = logger.FilePath()
		_ = q.UpdateRunLogFilePath(ctx, s.run.ID, logger.FilePath())
	}

	if s.root.ProjectID != nil {
		if project, err := q.GetProject(ctx, *s.root.ProjectID); err == nil && project.RepositoryUrl != "" {
			s.prepareWorktree(ctx, manager, project, worktree)
		}
	}

	if s.writer {
		s.workspace = worktree
	} else {
		// A reader works in a directory of its own and sees the project
		// read-only, so any number of them can run beside one writer.
		s.workspace = paths.RunWorkspaceDir(short, s.root.ID, s.run.ID)
		if info, err := os.Stat(worktree); err == nil && info.IsDir() {
			s.readOnly = append(s.readOnly, worktree)
		}
	}
	if err := os.MkdirAll(s.workspace, 0o755); err != nil {
		return fmt.Errorf("could not create the working directory: %w", err)
	}
	s.readOnly = append(s.readOnly, s.artifactDir)
	// What the human attached to the task is there to be read.
	if attachments, err := q.ListAttachmentsByTask(ctx, s.root.ID); err == nil && len(attachments) > 0 {
		s.readOnly = append(s.readOnly, paths.TaskUploadsDir(s.root.ID))
		for _, attachment := range attachments {
			s.attachments = append(s.attachments, attachment.FilePath)
		}
	}
	if s.run.WorkspacePath != s.workspace {
		s.run.WorkspacePath = s.workspace
		if err := q.UpdateRunWorkspacePath(ctx, s.run.ID, s.workspace); err != nil {
			return err
		}
	}
	return nil
}

// prepareWorktree makes sure the tree's git worktree exists and is current.
// Sessions of one tree may start together, so creating it is serialized.
func (s *executorSession) prepareWorktree(ctx context.Context, manager *filesystem.Manager, project db.Project, worktree string) {
	lock, _ := s.e.worktreeLocks.LoadOrStore(s.root.ID, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	repoDir := manager.GetProjectRepoPath(s.company, project)
	s.readOnly = append(s.readOnly, repoDir)
	keyPath, keyCleanup := filesystem.ResolveSSHKeyPathForCompany(ctx, s.e.q, loadSettings().BasePath, s.company)
	s.cleanups = append(s.cleanups, keyCleanup)
	git := gitpkg.NewGitManager(repoDir, keyPath)
	if project.GitHubInstallationID != 0 {
		if token, err := githubapp.TokenForProject(ctx, project); err == nil && token != "" {
			git.WithHTTPToken(token)
		} else if err != nil {
			s.info("GitHub App token error: " + err.Error())
		}
	}
	if err := git.Pull(ctx); err != nil {
		s.info("Warning: git pull failed: " + err.Error())
	}
	if _, err := os.Stat(filepath.Join(worktree, ".git")); os.IsNotExist(err) {
		branch := strings.TrimSpace(s.root.GitHubBranch)
		if branch == "" {
			branch = db.TaskGitBranch(s.root.RefKey, s.root.ID)
		}
		_ = os.RemoveAll(worktree)
		if err := git.CreateWorktree(ctx, repoDir, worktree, branch, "origin/"+s.root.EffectiveGitBaseBranch()); err != nil {
			s.info("Failed to create worktree: " + err.Error())
			return
		}
	}
	s.git = git
}

// prompts builds the session's opening: how to work, and the task with what
// it builds on and what an earlier attempt left behind.
func (s *executorSession) prompts(ctx context.Context) (system, user string) {
	q := s.e.q
	in := workflow.ExecutorInput{
		RolePrompt:   s.agent.SystemPrompt,
		TaskRef:      s.task.RefKey,
		TaskTitle:    s.task.Title,
		TaskType:     s.task.TaskType,
		Instructions: s.task.Description,
		Attempt:      s.run.Attempt,
	}
	if s.task.ParentID != nil {
		if parent, err := q.GetTask(ctx, *s.task.ParentID); err == nil {
			in.ParentContext = parent.Title
			if parent.RefKey != "" {
				in.ParentContext = parent.RefKey + " — " + parent.Title
			}
			if spec := strings.TrimSpace(parent.RefinedDescription); spec != "" {
				in.ParentContext += "\n\nSpecification:\n" + truncateText(spec, 4000)
			}
			if design := strings.TrimSpace(parent.Design); design != "" {
				in.ParentContext += "\n\nTechnical design:\n" + truncateText(design, 4000)
			}
		}
	}
	if prerequisites, err := q.ListPrerequisites(ctx, s.task.ID); err == nil {
		for _, prerequisite := range prerequisites {
			in.Prerequisites = append(in.Prerequisites, workflow.PrerequisiteResult{
				Title: prerequisite.Title, Type: prerequisite.TaskType, Status: prerequisite.Status,
				Verdict: prerequisite.ResultVerdict, Summary: prerequisite.ResultSummary,
				Details: truncateText(prerequisite.ResultDetails, 6000),
			})
		}
	}
	if s.run.Attempt > 1 {
		if steps, err := q.ListTaskStepsByKind(ctx, s.task.ID, models.StepCheckpoint); err == nil {
			for _, step := range steps {
				if step.RunID == nil || *step.RunID != s.run.ID {
					in.EarlierProgress = append(in.EarlierProgress, step.Result)
				}
			}
		}
		if decisions, err := q.ListDecisionsByTask(ctx, s.task.ID); err == nil {
			in.EarlierRecords = recordsOnFile(decisions)
		}
	}

	var environment strings.Builder
	fmt.Fprintf(&environment, "Working directory: %s\n", s.workspace)
	if len(s.readOnly) > 0 {
		fmt.Fprintf(&environment, "Readable but not writable: %s\n", strings.Join(s.readOnly, ", "))
	}
	if len(s.attachments) > 0 {
		fmt.Fprintf(&environment, "Files the human attached to the task: %s\n", strings.Join(s.attachments, ", "))
	}
	if s.writer && s.git != nil {
		fmt.Fprintf(&environment, "Git branch: %s (based on %s)\n", s.root.GitHubBranch, s.root.EffectiveGitBaseBranch())
	}
	in.Environment = strings.TrimSpace(environment.String())
	return workflow.ComposeExecutor(in)
}

func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "\n…(truncated)"
}

const stopLoopingMessage = "You are repeating the same actions and learning nothing new from them. Stop. Call `finish_work` now: use status `cannot_complete` or `failed`, say exactly what you tried, what came back each time, and what you would need to get further."

// gateUsage is the ledger context of the classifier calls of this session.
func (s *executorSession) gateUsage() callContext {
	usage := callContextFor(s.task, models.TaskPhaseExecute, "", &s.agent)
	runID := s.run.ID
	usage.RunID = &runID
	return usage
}

// sameRecords asks the classifier which new entries restate a record already
// on file. Without a classifier only word-for-word repeats are caught.
func (s *executorSession) sameRecords(ctx context.Context, fresh map[string]tools.RecordInput, existing []db.Decision) map[string]int64 {
	return s.gate.sameRecords(ctx, s.gateUsage(), fresh, existing)
}

// beforeTurn watches the session between turns. It stops a session that is
// going in circles, and demands a checkpoint when one is due: at the fixed
// interval, or at once when the classifier sees that something worth
// recording has just happened. It shows the executor what is already on
// record so it adds only what is new, and when nothing notable happened it
// asks only where the work stands.
func (s *executorSession) beforeTurn(ctx context.Context, history []aicli.Message) ([]aicli.Message, error) {
	if s.report != nil || s.turn.stopping {
		return nil, nil
	}
	total, _ := toolCalls(history)
	looping := repeatedCalls(history) >= repeatedCallLimit
	if s.gate != nil && !s.checkpoints.demanded && total > s.turn.judged {
		s.turn.judged = total
		verdict := s.gate.judgeTurns(ctx, s.gateUsage(), history)
		s.turn.notable = s.turn.notable || verdict.notable
		if verdict.looping {
			s.turn.loopHits++
		} else {
			s.turn.loopHits = 0
		}
		looping = looping || s.turn.loopHits >= maxLoopHits
	}
	if looping {
		s.turn.stopping = true
		s.info("The session is repeating itself; it was told to stop and report.")
		return []aicli.Message{{Role: "user", Content: stopLoopingMessage}}, nil
	}

	due := s.checkpoints.due(history)
	if !due && s.turn.notable {
		due = s.checkpoints.demand()
	}
	if !due {
		return nil, nil
	}
	// With a classifier watching, a checkpoint demanded only because the
	// interval is up, with nothing notable since the last one, asks for
	// progress alone: there is nothing new to record, so nothing to restate.
	progressOnly := s.gate != nil && !s.turn.notable
	s.checkpointTool.SetProgressOnly(progressOnly)
	if progressOnly {
		return []aicli.Message{{Role: "user", Content: "Checkpoint required before you continue. Call `checkpoint` now: say where the work stands and what you will do next."}}, nil
	}
	message := "Checkpoint required before you continue. Call `checkpoint` now: say where the work stands, and record only what is new since your last checkpoint — choices you made, approaches you abandoned, things you are assuming. Empty lists are correct if nothing new was decided."
	if decisions, err := s.e.q.ListDecisionsByTask(ctx, s.task.ID); err == nil {
		if onFile := recordsOnFile(decisions); onFile != "" {
			message += "\n\nAlready on record for this task (do not repeat these; to change one, name its ID in `revises`):\n" + onFile
		}
	}
	return []aicli.Message{{Role: "user", Content: message}}, nil
}

// toolsForTurn narrows a turn to one tool when the session must do one thing
// before anything else: report, once it has been told to stop, or checkpoint,
// when one is demanded.
func (s *executorSession) toolsForTurn([]aicli.Message) []string {
	if s.turn.stopping && s.report == nil {
		return []string{string(aicli.ToolFinishWork)}
	}
	if s.checkpoints.demanded {
		return []string{string(aicli.ToolCheckpoint)}
	}
	return nil
}

// onFinalText is called when the model answers without calling a tool, which
// would otherwise end the session. A session ends only by reporting, so the
// model is told what is expected, within limits.
func (s *executorSession) onFinalText(context.Context, []aicli.Message) ([]aicli.Message, error) {
	if s.report != nil {
		return nil, nil
	}
	if s.checkpoints.demanded {
		if s.checkpoints.missed() {
			return []aicli.Message{{Role: "user", Content: "You must call the `checkpoint` tool now, before anything else."}}, nil
		}
		return []aicli.Message{{Role: "user", Content: "Continue with the task."}}, nil
	}
	s.reminders++
	if s.reminders > maxFinishReminders {
		return nil, nil
	}
	return []aicli.Message{{Role: "user", Content: "The task is not finished until you call `finish_work` with your report. Continue working, or call `finish_work` now and say honestly how far you got."}}, nil
}

// onModelCall records each model round trip of the session in the usage
// ledger, located by its position in the session's log.
func (s *executorSession) onModelCall(call aicli.ModelCall) {
	usage := callContextFor(s.task, models.TaskPhaseExecute, "executor_turn", &s.agent)
	runID := s.run.ID
	usage.RunID = &runID
	if call.Sequence > 0 {
		sequence := call.Sequence
		usage.LogSeq = &sequence
	}
	status, errText := models.LLMCallOK, ""
	if call.Err != nil {
		status, errText = models.LLMCallError, call.Err.Error()
	}
	recordCall(context.Background(), s.e.q, usage, s.target, call.Model, call.Usage, call.Duration, status, errText)
}

// pause stops a session at a turn boundary for a planned restart, leaving a
// cursor into its log from which it is resumed.
func (s *executorSession) pause(agent *aicli.Agent) (status, message string) {
	if s.logger == nil {
		return "failed", "could not pause for a restart: the session has no log to resume from"
	}
	if err := s.logger.Sync(); err != nil {
		return "failed", fmt.Sprintf("could not pause for a restart: %v", err)
	}
	sequence := agent.ConversationSequence()
	if sequence <= 0 {
		sequence = s.run.Recovery.CheckpointSequence
	}
	if sequence <= 0 {
		return "failed", "could not pause for a restart: nothing was logged yet"
	}
	if err := s.e.q.PauseRunWithMetadata(context.Background(), s.run.ID, sequence, resumeAfterRestart, "", "", string(db.CheckpointPhaseBeforeTools)); err != nil {
		return "failed", fmt.Sprintf("could not pause for a restart: %v", err)
	}
	s.info("Session paused for a server restart")
	return db.RunStatusPaused, ""
}

func (s *executorSession) info(message string) {
	if s.logger != nil {
		s.logger.LogInfo(message)
	}
}

func (s *executorSession) outcome(status, endReason, summary string) {
	if s.logger == nil {
		return
	}
	reported := ""
	if s.report != nil {
		reported = s.report.Status
	}
	s.logger.LogOutcome(status, endReason, reported, s.agent.Name, s.task.ID, summary)
}

// resumePausedExecutors continues the sessions a planned restart paused.
func (e *NativeEngine) resumePausedExecutors(ctx context.Context) {
	if err := e.q.ReclaimExpiredResumeLeases(ctx, time.Now()); err != nil {
		fmt.Printf("Warning: could not reclaim expired resume leases: %v\n", err)
	}
	runs, err := e.q.GetRunsByRecoveryStates(ctx, []string{db.RunStatusPaused})
	if err != nil {
		fmt.Printf("Warning: could not list paused sessions: %v\n", err)
		return
	}
	for _, run := range runs {
		task, err := e.q.GetTask(ctx, run.TaskID)
		if err != nil || task.RunID == nil || *task.RunID != run.ID || task.WaitingOn != models.TaskWaitRun {
			continue
		}
		owner := e.driver.instance
		claimed, err := e.q.ClaimRunForResume(ctx, run.ID, owner, resumeAfterRestart, db.RunStatusPaused,
			time.Now().Add(taskLeaseTTL), []string{db.RunStatusPaused}, run.Recovery.CheckpointSequence)
		if err != nil || !claimed {
			continue
		}
		if err := e.q.MarkRunResumeStarted(ctx, run.ID, owner); err != nil {
			continue
		}
		// A resumed session takes a slot even if that exceeds the limit for a
		// moment: it was already running before the restart.
		e.executorSlots.Add(1)
		runID := run.ID
		e.runs.active.Add(1)
		go func() {
			defer e.runs.active.Done()
			defer e.releaseExecutorSlot()
			e.runExecutor(runID, true)
		}()
	}
}

// reapSessions fails executor sessions that stopped heartbeating and that no
// goroutine of this process owns, and returns the tasks waiting on them. A
// session whose process died is found here; its task then retries.
func (e *NativeEngine) reapSessions(ctx context.Context) []int32 {
	stale, err := e.q.GetStaleRunningRuns(ctx, e.staleAfter)
	if err != nil {
		return nil
	}
	var tasks []int32
	for _, run := range stale {
		if _, alive := e.runs.cancelFuncs.Load(run.ID); alive {
			continue
		}
		task, err := e.q.GetTask(ctx, run.TaskID)
		if err != nil || task.RunID == nil || *task.RunID != run.ID || task.WaitingOn != models.TaskWaitRun {
			continue
		}
		if err := e.q.UpdateRunLog(ctx, run.ID, sessionStoppedResponding, "failed"); err != nil {
			continue
		}
		e.hub.BroadcastEventForCompany(task.CompanyID, "run_ended", map[string]interface{}{"run_id": run.ID, "status": "failed"})
		tasks = append(tasks, task.ID)
	}
	return tasks
}
