package engine

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"
)

const statusReportFreshness = 10 * time.Minute

var orchestratorPrompt = agentconfig.MustPrompt("task_orchestrator.md")

// createTaskOrchestrator creates the task-owned root run before its worker
// starts. Worker and delegated runs then form a normal child tree beneath it.
func (e *NativeEngine) createTaskOrchestrator(ctx context.Context, task db.Task, agent db.Agent) (db.Run, db.LLMProvider, string, bool, bool) {
	uid := e.ownerUserIDForCompany(ctx, task.CompanyID)
	provider, model, err := e.resolveRequiredPurposeModel(ctx, uid, db.PurposeTaskOrchestrator)
	if err != nil {
		// Leave a visible failed orchestration attempt and block the task. The
		// assigned agent is never started until the user fixes configuration and
		// explicitly restarts/unblocks the task.
		ended := time.Now()
		failed := db.Run{TaskID: task.ID, AgentID: agent.ID, Kind: db.RunKindTaskOrchestrator, Status: "failed", StartedAt: ended, EndedAt: &ended, ResultExplanation: "ORCHESTRATOR_MODEL_REQUIRED: " + err.Error()}
		if created, createErr := e.q.CreateRun(ctx, failed); createErr == nil {
			_ = e.q.UpdateRunLog(ctx, created.ID, failed.ResultExplanation, "failed")
			_ = e.q.SetTaskOrchestratorRun(ctx, task.ID, created.ID)
		}
		if current, getErr := e.q.GetTask(ctx, task.ID); getErr == nil {
			current.Status = db.TaskStatusBlocked
			_, _ = e.q.UpdateTask(ctx, current)
		}
		_, _ = e.q.CreateComment(ctx, db.Comment{TaskID: task.ID, AuthorType: "system", CommentType: "orchestrator_preflight_failed", Content: "ORCHESTRATOR_MODEL_REQUIRED: " + err.Error()})
		return db.Run{}, db.LLMProvider{}, "", false, false
	}
	if existing, err := e.q.GetOrchestratorRun(ctx, task.ID); err == nil && existing.ID != 0 && (existing.Status == "running" || existing.Status == "waiting") {
		return existing, provider, model, true, false
	}
	orchestrator := db.Run{
		TaskID: task.ID, AgentID: agent.ID, Kind: db.RunKindTaskOrchestrator, Status: "running", StartedAt: time.Now(),
		Name: fmt.Sprintf("%s-orchestrator", task.RefKey), Title: "Task orchestration",
	}
	created, err := e.q.CreateRun(ctx, orchestrator)
	if err != nil {
		return db.Run{}, db.LLMProvider{}, "", false, false
	}
	if err := e.q.SetRunRootID(ctx, created.ID, created.ID); err != nil {
		return db.Run{}, db.LLMProvider{}, "", false, false
	}
	if err := e.q.SetTaskOrchestratorRun(ctx, task.ID, created.ID); err != nil {
		_ = e.q.UpdateRunLog(ctx, created.ID, err.Error(), "failed")
		return db.Run{}, db.LLMProvider{}, "", false, false
	}
	return created, provider, model, true, true
}

func (e *NativeEngine) startTaskOrchestrator(orchestrator db.Run, task db.Task, provider db.LLMProvider, model string) {
	if !e.runs.tryStartOrchestrator(orchestrator.ID) {
		return
	}
	// The orchestrator is a long-lived sidecar rather than a normal agent
	// session, but it still owns a cancellable goroutine. Register it in the
	// same process-local lifecycle registry as regular runs so E2E resets,
	// shutdowns, and explicit stop requests can drain it deterministically.
	runCtx, cancel := e.runs.contextWithDrain(context.Background())
	e.runs.cancelFuncs.Store(orchestrator.ID, cancel)
	e.runs.activeRoots.Add(1)
	go func() {
		defer e.runs.activeRoots.Done()
		defer cancel()
		defer e.runs.cancelFuncs.Delete(orchestrator.ID)
		defer e.runs.stopOrchestrator(orchestrator.ID)
		e.runOrchestrator(runCtx, orchestrator, task, provider, model)
	}()
}

// buildOrchestratorSystemPrompt gives the sidecar the same authoritative
// context a worker would receive, plus the company roster it is allowed to
// select from. Keeping this in the system prompt means every activation can
// reason about the task before it chooses a worker or recovery action.
func (e *NativeEngine) buildOrchestratorSystemPrompt(ctx context.Context, task db.Task) (string, error) {
	agents, err := e.q.ListAgentsByCompany(ctx, task.CompanyID)
	if err != nil {
		return "", fmt.Errorf("list available agents: %w", err)
	}
	var b strings.Builder
	b.WriteString(orchestratorPrompt)
	b.WriteString("\n\nAuthoritative task context (use this to plan the worker execution):\n")
	fmt.Fprintf(&b, "Company: %s (id: %d, short name: %s)\n", task.Company.Name, task.Company.ID, task.Company.ShortName)
	fmt.Fprintf(&b, "Company description: %s\n", valueOrUnavailable(task.Company.Description))
	if task.Project != nil {
		fmt.Fprintf(&b, "Project: %s (id: %d)\nProject description: %s\n", task.Project.Name, task.Project.ID, valueOrUnavailable(task.Project.Description))
		fmt.Fprintf(&b, "Project repository: %s\n", valueOrUnavailable(task.Project.RepositoryUrl))
		fmt.Fprintf(&b, "Project workspace: %s\nProject default branch: %s\n", valueOrUnavailable(task.Project.WorkspaceFolder), valueOrUnavailable(task.Project.GitHubDefaultBranch))
	} else {
		b.WriteString("Project: none assigned\n")
	}
	if task.SprintID != 0 {
		fmt.Fprintf(&b, "Sprint: %s (id: %d)\nSprint goal: %s\n", task.Sprint.Name, task.Sprint.ID, valueOrUnavailable(task.Sprint.Goal))
		if task.Sprint.StartDate != nil || task.Sprint.EndDate != nil {
			fmt.Fprintf(&b, "Sprint dates: %s to %s\n", formatOptionalDate(task.Sprint.StartDate), formatOptionalDate(task.Sprint.EndDate))
		}
	} else {
		b.WriteString("Sprint: none assigned\n")
	}
	fmt.Fprintf(&b, "Task: %s (id: %d, ref: %s)\n", task.Title, task.ID, task.RefKey)
	mode := executionModeImplementation
	if task.Status == db.TaskStatusRefinement {
		mode = executionModeRefinement
	}
	fmt.Fprintf(&b, "Execution mode: %s\nTask status: %s\nPriority: %s\n", mode, valueOrUnavailable(task.Status), valueOrUnavailable(task.Priority))
	fmt.Fprintf(&b, "Task description: %s\n", valueOrUnavailable(task.Description))
	fmt.Fprintf(&b, "Refined description: %s\n", valueOrUnavailable(task.RefinedDescription))
	fmt.Fprintf(&b, "Acceptance criteria: %s\n", valueOrUnavailable(formatSpecItems(task.AcceptanceCriteria)))
	fmt.Fprintf(&b, "Test cases: %s\n", valueOrUnavailable(formatSpecItems(task.TestCases)))
	fmt.Fprintf(&b, "Git branch: %s\nBase branch: %s\n", valueOrUnavailable(task.GitHubBranch), valueOrUnavailable(task.GitBaseBranch))
	fmt.Fprintf(&b, "Due date: %s\nArchived: %t\nGitHub PR: #%d %s\n", formatOptionalDate(task.DueDate), task.IsArchived, task.GitHubPRNumber, valueOrUnavailable(task.GitHubPRURL))
	if task.ParentID != nil {
		fmt.Fprintf(&b, "Parent task id: %d\n", *task.ParentID)
	}
	if task.AgentID != nil {
		if assigned, agentErr := e.q.GetAgent(ctx, *task.AgentID); agentErr == nil {
			fmt.Fprintf(&b, "Product-owner assignment: %s (%s)\n", assigned.Name, valueOrUnavailable(assigned.Description))
		}
	}
	if summaries, relErr := e.q.ListTaskRelationSummaries(ctx, []int32{task.ID}); relErr == nil {
		if relations := formatTaskRelations(summaries[task.ID]); relations != "" {
			b.WriteString("Task relations:\n")
			b.WriteString(relations)
			b.WriteByte('\n')
		}
	}
	b.WriteString("\nAvailable agents for run_new_session (choose by name, not ID):\n")
	if len(agents) == 0 {
		b.WriteString("- No agents are currently available; report the blocker instead of guessing.\n")
	} else {
		for _, agent := range agents {
			name := agent.Name
			if strings.TrimSpace(name) == "" {
				name = agent.RoleKey
			}
			fmt.Fprintf(&b, "- %s", name)
			if agent.RoleKey != "" && agent.RoleKey != name {
				fmt.Fprintf(&b, " (role: %s)", agent.RoleKey)
			}
			if agent.Description != "" {
				fmt.Fprintf(&b, ": %s", agent.Description)
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n\n" + strings.TrimSpace(agentconfig.MustPrompt("utils/orchestrator_start.md")))
	b.WriteString("\n\nReserved one-time worker: call run_new_session with agent_name=Worker, a short title, and a bounded prompt for repository verification, git commands, artifact work, or other auxiliary jobs. This starts the helper-worker runtime with the helper-worker model and tools; do not assign task implementation or ownership to it.\nWhen all required worker sessions are terminal, all human questions are answered, and the task is genuinely complete, call finish_task with a concise verification summary. A prose completion message does not finish the task.\n")
	return b.String(), nil
}

func valueOrUnavailable(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(not provided)"
	}
	return value
}

func formatOptionalDate(value *time.Time) string {
	if value == nil {
		return "(not set)"
	}
	return value.Format("2006-01-02")
}

// ResumeWaitingOrchestrators reattaches sidecars after a process restart. A
// quiet sidecar remains dormant; pending worker events are consumed by its
// next activation rather than being lost with the old goroutine.
func (e *NativeEngine) ResumeWaitingOrchestrators(ctx context.Context) {
	runs, err := e.q.ListWaitingOrchestrators(ctx)
	if err != nil {
		return
	}
	for _, orch := range runs {
		task, err := e.q.GetTask(ctx, orch.TaskID)
		if err != nil {
			continue
		}
		if task.AgentID == nil {
			continue
		}
		provider, model, err := e.resolveRequiredPurposeModel(ctx, e.ownerUserIDForCompany(ctx, task.CompanyID), db.PurposeTaskOrchestrator)
		// A model-group target is a synthetic provider (ID zero) that points
		// at the local gateway, so provider.ID is not a validity check here.
		if err != nil || model == "" {
			continue
		}
		e.startTaskOrchestrator(orch, task, provider, model)
	}
}

func (e *NativeEngine) runOrchestrator(ctx context.Context, orchestrator db.Run, task db.Task, provider db.LLMProvider, model string) {
	markCanceled := func() {
		_ = e.q.UpdateRunLog(context.Background(), orchestrator.ID, "orchestrator canceled", "canceled")
		e.hub.BroadcastEventForCompany(task.CompanyID, "run_ended", map[string]interface{}{"run_id": orchestrator.ID, "status": "canceled"})
	}
	markDrained := func() {
		// The sidecar has no resumable turn checkpoint. Waiting is the durable
		// restart marker; ResumeWaitingOrchestrators reattaches it on the next
		// process boot without manufacturing a terminal cancellation.
		_ = e.q.SetRunWaitState(context.Background(), orchestrator.ID, "paused for process restart")
	}
	if ctx.Err() != nil {
		if e.runs.draining.Load() {
			markDrained()
		} else {
			markCanceled()
		}
		return
	}
	// A task may have moved to Done, Canceled, or another non-executable state
	// between scheduling and starting this sidecar. In that case the
	// orchestrator must not create a model turn or become a stale candidate.
	if current, getErr := e.q.GetTask(ctx, task.ID); getErr == nil && current.Status != db.TaskStatusInProgress {
		if current.Status == db.TaskStatusBlocked && e.humanInputPending(ctx, task.ID) {
			_ = e.q.SetRunWaitState(ctx, orchestrator.ID, "awaiting_human_input")
		} else {
			// A sidecar that has no executable task is dormant, not active. Keep
			// the durable row terminal so restarts and E2E resets do not mistake a
			// goroutine that already exited for a cancelable waiting run.
			_ = e.q.UpdateRunLog(ctx, orchestrator.ID, "task is not in progress", "completed")
		}
		_ = e.q.UnlockTaskRun(ctx, task.ID)
		return
	}
	// The orchestrator is a passive sidecar, but its liveness still needs a
	// real heartbeat while it is inside a long provider call. A heartbeat does
	// not count as a conversation turn: silence detection below reads the
	// latest persisted log entry instead.
	heartbeatStop := e.startOrchestratorHeartbeat(task.ID, orchestrator.ID, orchestratorHeartbeatInterval)
	defer heartbeatStop()
	logger, err := logging.NewSessionLoggerWithHub(loadSettings().BasePath, task.Company.ShortName, task.ID, orchestrator.ID, orchestrator.ID, e.hub.ForCompany(task.CompanyID), e.q)
	if err != nil {
		_ = e.q.UpdateRunLog(ctx, orchestrator.ID, err.Error(), "failed")
		return
	}
	defer logger.Close()
	defer e.q.UnlockTaskRun(context.Background(), task.ID)
	_ = e.q.UpdateRunLogFilePath(ctx, orchestrator.ID, logger.FilePath())
	systemPrompt, promptErr := e.buildOrchestratorSystemPrompt(ctx, task)
	if promptErr != nil {
		_ = e.q.UpdateRunLog(ctx, orchestrator.ID, promptErr.Error(), "failed")
		return
	}
	systemPrompt += fmt.Sprintf("\n\nYour orchestrator session id is %d. It is not a worker session: never pass this id to get_session, send_message_to_session, stop_session, fork_session, or run_new_session source_session_id. Use get_session_list to inspect worker sessions only.\n", orchestrator.ID)

	apiKey, _ := secrets.Default().Decrypt(provider.ApiKeyEncrypted)
	client := aicli.NewClient(provider.BaseUrl, apiKey, model)
	var gatewayAuth runGatewayAuth
	if isModelGroupProxyBaseURL(provider.BaseUrl) {
		gatewayAuth = runGatewayAuth{runID: orchestrator.ID, token: runtokens.Default().Issue(orchestrator.ID)}
		defer runtokens.Default().Revoke(orchestrator.ID)
		if authErr := gatewayAuth.configure(client, provider); authErr != nil {
			_ = e.q.UpdateRunLog(ctx, orchestrator.ID, authErr.Error(), "failed")
			return
		}
	}
	callbacks := tools.OrchestratorCallbacks{
		OrchestratorSessionID: orchestrator.ID,
		GetSessionList: func(c context.Context) ([]tools.ManagedSessionSummary, error) {
			return e.orchestratorSessions(c, orchestrator.ID)
		},
		GetSession: func(c context.Context, id int32) (tools.ManagedSessionDetails, error) {
			return e.orchestratorSessionDetails(c, task, orchestrator.ID, id)
		},
		SendMessage: func(c context.Context, id int32, message string) (string, error) {
			return e.orchestratorSendMessage(c, task, orchestrator.ID, id, message)
		},
		RunNewSession: func(c context.Context, source *int32, agentName, title, prompt string) (string, error) {
			return e.orchestratorRunNew(c, task, orchestrator.ID, source, agentName, title, prompt)
		},
		StopSession: func(c context.Context, id int32, reason string) (string, error) {
			return e.orchestratorStop(c, orchestrator.ID, id, reason)
		},
		ForkSession: func(c context.Context, sessionID int32, messageID int64) (string, error) {
			return e.orchestratorFork(c, orchestrator.ID, sessionID, messageID)
		},
		FinishTask: func(c context.Context, summary string) (string, error) {
			return e.orchestratorFinishTask(c, task, orchestrator.ID, summary)
		},
		AnswerMessage: func(c context.Context, messageID int64, answer string) (string, error) {
			return e.answerRoutedMessage(c, orchestrator, messageID, answer)
		},
		AskCEO: func(c context.Context, taskID int32, message string) (string, error) {
			if taskID != task.ID {
				return "", fmt.Errorf("ask_ceo task_id %d is outside orchestrator task %d", taskID, task.ID)
			}
			return e.askCEO(c, orchestrator, task, message)
		},
	}
	registry := tools.NewOrchestratorRegistry(callbacks)
	logger.LogInfo("Effective tools: " + strings.Join(registry.Names(), ", "))

	ai := aicli.New(aicli.Config{
		Client: client, Registry: registry, Mode: aicli.ModeMessageHistory,
		ProviderName: provider.Name, AgentName: "Task Orchestrator",
		Queries: e.q, RunID: orchestrator.ID, Logger: logger,
	})

	lastFingerprint := ""
	first := true
	for {
		sessions, listErr := e.orchestratorSessions(ctx, orchestrator.ID)
		if listErr != nil {
			_ = e.q.UpdateRunLog(ctx, orchestrator.ID, listErr.Error(), "failed")
			return
		}
		events, _ := e.q.ListPendingRunEvents(ctx, task.ID)
		inbound, _ := e.q.ListUnconsumedEventsForTarget(ctx, orchestrator.ID, db.RunEventTypeSessionMessage, db.RunEventTypeSessionAnswer)
		hasPendingMessage := false
		for _, event := range inbound {
			if event.EventType == db.RunEventTypeSessionMessage {
				hasPendingMessage = true
				break
			}
		}
		if hasPendingMessage {
			tools.RegisterOrchestratorAnswerMessage(registry, callbacks.AnswerMessage)
		} else {
			registry.Unregister(string(aicli.ToolAnswerMessage))
		}
		taskNow, _ := e.q.GetTask(ctx, task.ID)
		humanPending := e.humanInputPending(ctx, task.ID)
		fingerprint := orchestratorFingerprintWithTask(sessions, taskNow.Status, humanPending)
		if humanPending && taskNow.Status == db.TaskStatusBlocked {
			// The ask_human tool owns the wait. Keep the sidecar durable and
			// dormant; the human reply handler will change the task state and
			// enqueue the only event needed to activate it again.
			first = false
			lastFingerprint = fingerprint
			_ = e.q.SetRunWaitState(ctx, orchestrator.ID, "awaiting_human_input")
			if !waitForOrchestrator(ctx, 200*time.Millisecond) {
				if e.runs.draining.Load() {
					markDrained()
				} else {
					markCanceled()
				}
				return
			}
			continue
		}
		if taskNow.Status != db.TaskStatusInProgress {
			// Terminal/non-executable task state is control-plane truth. Do not
			// let a late worker event or watchdog wake create more model work.
			if taskNow.Status == db.TaskStatusBlocked && humanPending {
				_ = e.q.SetRunWaitState(ctx, orchestrator.ID, "awaiting_human_input")
			} else {
				_ = e.q.UpdateRunLog(ctx, orchestrator.ID, "task is not in progress", "completed")
			}
			return
		}
		// Task completion is authoritative. Do not spend another model turn
		// reacting to the fingerprint change caused by the final worker or
		// finish_task update. This also prevents a late lifecycle event from
		// keeping a completed parent run alive indefinitely.
		if isOrchestratorTaskComplete(taskNow.Status) && allWorkerSessionsTerminal(sessions) {
			_ = e.q.UpdateRunLog(ctx, orchestrator.ID, "worker execution is terminal", "completed")
			return
		}
		if !first && fingerprint == lastFingerprint && len(events) == 0 && len(inbound) == 0 {
			// An in-review handoff gets one model activation above so the
			// orchestrator can launch the next stage. If that activation makes
			// no change, the terminal task state and terminal worker tree mean
			// there is no remaining orchestration work.
			if isTerminalTaskStatus(taskNow.Status) && allWorkerSessionsTerminal(sessions) {
				_ = e.q.UpdateRunLog(ctx, orchestrator.ID, "worker execution is terminal", "completed")
				return
			}
			_ = e.q.SetRunWaitState(ctx, orchestrator.ID, "waiting for worker lifecycle event")
			if !waitForOrchestrator(ctx, 2*time.Second) {
				if e.runs.draining.Load() {
					markDrained()
				} else {
					markCanceled()
				}
				return
			}
			continue
		}
		wasFirst := first
		first = false
		lastFingerprint = fingerprint
		_ = e.q.UpdateRunLog(ctx, orchestrator.ID, "", "running")

		state, _ := json.Marshal(sessions)
		message := "Initial worker execution snapshot:\n" + string(state)
		if !wasFirst {
			message = "Worker lifecycle state changed. Re-inspect sessions and take only a justified recovery action.\n" + string(state)
		}
		if len(events) > 0 {
			eventJSON, _ := json.Marshal(events)
			message += "\nRouted lifecycle events since the last activation:\n" + string(eventJSON)
		}
		if len(inbound) > 0 {
			messageJSON, _ := json.Marshal(inbound)
			message += "\nIncoming routed messages (answer each required message with answer_message and its exact ID):\n" + string(messageJSON)
		}
		_, runErr := ai.RunWithMessages(ctx, systemPrompt, []aicli.Message{{Role: "user", Content: message}})
		if errors.Is(runErr, context.Canceled) || ctx.Err() != nil {
			if e.runs.draining.Load() {
				markDrained()
			} else {
				markCanceled()
			}
			return
		}
		if runErr != nil {
			_ = e.q.UpdateRunLog(ctx, orchestrator.ID, runErr.Error(), "failed")
			return
		}
		// Consume only after the orchestrator has successfully observed the
		// snapshot. If the provider fails, the durable status/lifecycle events
		// remain available for the next activation instead of being lost.
		if len(events) > 0 {
			ids := make([]int64, 0, len(events))
			for _, event := range events {
				ids = append(ids, event.ID)
			}
			if consumeErr := e.q.ConsumeRunEvents(ctx, ids); consumeErr != nil {
				_ = e.q.UpdateRunLog(ctx, orchestrator.ID, consumeErr.Error(), "failed")
				return
			}
		}
		answerIDs := make([]int64, 0)
		for _, event := range inbound {
			if event.EventType == db.RunEventTypeSessionAnswer {
				answerIDs = append(answerIDs, event.ID)
			}
		}
		if len(answerIDs) > 0 {
			if consumeErr := e.q.ConsumeRunEvents(ctx, answerIDs); consumeErr != nil {
				_ = e.q.UpdateRunLog(ctx, orchestrator.ID, consumeErr.Error(), "failed")
				return
			}
		}
		_ = e.q.SetRunWaitState(ctx, orchestrator.ID, "waiting for worker lifecycle event")
	}
}

func waitForOrchestrator(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// startOrchestratorHeartbeat keeps a live sidecar visible to the liveness
// monitor without turning a heartbeat into a conversation/history entry. The
// task status check is intentional: once the control plane leaves in-progress,
// the sidecar is dormant and must not keep itself alive or be recovered.
func (e *NativeEngine) startOrchestratorHeartbeat(taskID, runID int32, interval time.Duration) func() {
	if interval <= 0 {
		interval = orchestratorHeartbeatInterval
	}
	done := make(chan struct{})
	if current, err := e.q.GetTask(context.Background(), taskID); err == nil && current.Status == db.TaskStatusInProgress {
		_ = e.q.TouchRunLastMessageTime(context.Background(), runID)
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				current, err := e.q.GetTask(context.Background(), taskID)
				if err == nil && current.Status == db.TaskStatusInProgress {
					_ = e.q.TouchRunLastMessageTime(context.Background(), runID)
				}
			}
		}
	}()
	return func() { close(done) }
}

func (e *NativeEngine) orchestratorFinishTask(ctx context.Context, task db.Task, orchestratorRunID int32, summary string) (string, error) {
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("summary is required")
	}
	if e.humanInputPending(ctx, task.ID) {
		return "", fmt.Errorf("task %d still has an unanswered human question", task.ID)
	}
	sessions, err := e.orchestratorSessions(ctx, orchestratorRunID)
	if err != nil {
		return "", err
	}
	if !allWorkerSessionsTerminal(sessions) {
		return "", fmt.Errorf("task %d still has active worker sessions", task.ID)
	}
	current, err := e.q.GetTask(ctx, task.ID)
	if err != nil {
		return "", err
	}
	if current.Status != db.TaskStatusDone {
		previous := current.Status
		current.Status = db.TaskStatusDone
		if _, err := e.q.UpdateTask(ctx, current); err != nil {
			return "", err
		}
		e.broadcastTaskStatus(current, previous, current.Status, nil)
	}
	return fmt.Sprintf("task %d marked done: %s", task.ID, strings.TrimSpace(summary)), nil
}

func (e *NativeEngine) orchestratorSessions(ctx context.Context, orchestratorRunID int32) ([]tools.ManagedSessionSummary, error) {
	runs, err := e.q.ListOrchestratorSessions(ctx, orchestratorRunID)
	if err != nil {
		return nil, err
	}
	out := make([]tools.ManagedSessionSummary, 0, len(runs))
	for _, r := range runs {
		if r.ID == orchestratorRunID || r.Kind == db.RunKindTaskOrchestrator {
			continue
		}
		out = append(out, managedSessionSummary(r))
	}
	return out, nil
}

func (e *NativeEngine) orchestratorSessionRun(ctx context.Context, orchestratorRunID, id int32) (db.Run, error) {
	r, err := e.q.GetRun(ctx, id)
	if err != nil {
		return db.Run{}, err
	}
	valid := r.ID != orchestratorRunID && r.RootRunID != nil && *r.RootRunID == orchestratorRunID
	if !valid {
		return db.Run{}, fmt.Errorf("session %d is outside the orchestrator worker tree", id)
	}
	return r, nil
}

func (e *NativeEngine) orchestratorSessionDetails(ctx context.Context, task db.Task, orchestratorRunID, id int32) (tools.ManagedSessionDetails, error) {
	r, err := e.orchestratorSessionRun(ctx, orchestratorRunID, id)
	if err != nil {
		return tools.ManagedSessionDetails{}, err
	}
	latest, err := e.orchestratorSessionLastRunStatus(ctx, task, orchestratorRunID, id)
	if err != nil {
		return tools.ManagedSessionDetails{}, err
	}
	reports, err := e.q.ListRunStatusReports(ctx, id)
	if err != nil {
		return tools.ManagedSessionDetails{}, err
	}
	history := make([]tools.ManagedSessionRunStatus, 0, len(reports))
	for _, report := range reports {
		history = append(history, tools.ManagedSessionRunStatus{
			Status:     report.Status,
			ReportedAt: report.ReportedAt.Format(time.RFC3339Nano),
			MessageID:  report.MessageID,
		})
	}
	var latestPtr *tools.ManagedSessionStatusReport
	if latest.LastReportedAt != "" || latest.LastReportedStatus != "" || len(latest.ChildStatuses) > 0 {
		latestPtr = &latest
	}
	return tools.ManagedSessionDetails{
		ManagedSessionSummary: managedSessionSummary(r),
		LastRunStatus:         latestPtr,
		RunStatusHistory:      history,
	}, nil
}

func managedSessionSummary(r db.Run) tools.ManagedSessionSummary {
	last := ""
	if r.LastMessageTime != nil {
		last = r.LastMessageTime.Format(time.RFC3339Nano)
	}
	return tools.ManagedSessionSummary{
		ID:                r.ID,
		Name:              r.Name,
		Title:             r.Title,
		TaskID:            r.TaskID,
		AgentID:           r.AgentID,
		AgentName:         managedRunAgentName(r),
		ParentSessionID:   r.ParentRunID,
		LifecycleStatus:   r.Status,
		LastMessageTime:   last,
		WaitReason:        r.Recovery.WaitReason,
		RecoveryAttempts:  r.Recovery.RecoveryAttempts,
		StopCause:         r.Recovery.StopCause,
		ResultDescription: r.ResultDescription,
		Error:             r.LogContent,
	}
}

func managedRunAgentName(r db.Run) string {
	if r.Kind == db.RunKindHelperWorker {
		return "Worker"
	}
	if r.Agent.Name != "" {
		return r.Agent.Name
	}
	return r.Name
}

func (e *NativeEngine) requestWorkerStatus(ctx context.Context, task db.Task, orchestratorRunID, sessionID int32) (bool, error) {
	if _, err := e.orchestratorSessionRun(ctx, orchestratorRunID, sessionID); err != nil {
		return false, err
	}
	now := time.Now()
	recent, err := e.q.HasRecentRunEvent(ctx, sessionID, db.RunEventTypeStatusRefresh, now.Add(-statusReportFreshness))
	if err != nil {
		return false, err
	}
	if recent {
		return true, nil
	}
	window := now.Unix() / int64(statusReportFreshness/time.Second)
	payload, _ := json.Marshal(map[string]interface{}{
		"task_id": task.ID,
		"message": "Report your current stage and any blocker using report_status before continuing.",
	})
	err = e.q.EnqueueRunEvent(ctx, db.RunEvent{
		TaskID: task.ID, RunID: sessionID, EventType: db.RunEventTypeStatusRefresh, Payload: string(payload),
		DedupeKey: fmt.Sprintf("run:%d:status-refresh:%d", sessionID, window),
	})
	return true, err
}

// orchestratorSendMessage routes a message to another managed session and
// waits for its correlated answer.
func (e *NativeEngine) orchestratorSendMessage(ctx context.Context, task db.Task, orchestratorID, sessionID int32, message string) (string, error) {
	target, err := e.orchestratorSessionRun(ctx, orchestratorID, sessionID)
	if err != nil {
		return "", err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "", fmt.Errorf("message is required")
	}
	if isTerminalRunStatus(target.Status) || target.Status == db.RunStatusPaused {
		return e.orchestratorMessageTerminalSession(ctx, task, orchestratorID, target, message)
	}
	return e.orchestratorSendMessageToRun(ctx, task.ID, orchestratorID, sessionID, message)
}

func (e *NativeEngine) orchestratorSendMessageToRun(ctx context.Context, taskID int32, sourceRunID, targetRunID int32, message string) (string, error) {
	payload, _ := json.Marshal(db.NewSessionMessage(message))
	event, err := e.q.EnqueueRoutedEvent(ctx, taskID, sourceRunID, targetRunID, db.RunEventTypeSessionMessage, string(payload), fmt.Sprintf("orchestrator-message:%d:%d:%d", sourceRunID, targetRunID, time.Now().UnixNano()))
	if err != nil {
		return "", fmt.Errorf("queue message for session %d: %w", targetRunID, err)
	}
	for {
		if answer, findErr := e.q.FindAnswerForMessage(ctx, event.ID); findErr == nil {
			return answer.Payload, nil
		}
		if target, runErr := e.q.GetRun(ctx, targetRunID); runErr == nil && isTerminalRunStatus(target.Status) {
			return "", fmt.Errorf("target session %d ended as %s before answering", targetRunID, target.Status)
		}
		if err := waitForRoutedAnswerPoll(ctx); err != nil {
			return "", err
		}
	}
}

func (e *NativeEngine) orchestratorMessageTerminalSession(ctx context.Context, task db.Task, orchestratorID int32, source db.Run, message string) (string, error) {
	agent, err := e.q.GetAgent(ctx, source.AgentID)
	if err != nil {
		return "", fmt.Errorf("load terminal session agent: %w", err)
	}
	replacement, err := e.createOrchestratorChildRun(ctx, task, orchestratorID, agent, "Terminal session replacement")
	if err != nil {
		return "", fmt.Errorf("create terminal-session replacement: %w", err)
	}
	payload, _ := json.Marshal(db.NewSessionMessage(message))
	event, err := e.q.EnqueueRoutedEvent(ctx, task.ID, orchestratorID, replacement.ID, db.RunEventTypeSessionMessage, string(payload), fmt.Sprintf("orchestrator-terminal-replacement:%d:%d:%d", source.ID, replacement.ID, time.Now().UnixNano()))
	if err != nil {
		_ = e.q.UpdateRunLog(ctx, replacement.ID, err.Error(), "failed")
		return "", fmt.Errorf("queue message for replacement session %d: %w", replacement.ID, err)
	}
	e.startOrchestratorChildSession(task, agent, replacement, "Answer the task owner's routed question using the existing design context.")
	answer, err := e.waitForRoutedAnswer(ctx, event.ID, replacement.ID)
	if err != nil {
		return "", fmt.Errorf("terminal session %d replacement %d: %w", source.ID, replacement.ID, err)
	}
	return answer, nil
}

func (e *NativeEngine) waitForRoutedAnswer(ctx context.Context, eventID int64, targetRunID int32) (string, error) {
	for {
		if answer, findErr := e.q.FindAnswerForMessage(ctx, eventID); findErr == nil {
			return answer.Payload, nil
		}
		if target, runErr := e.q.GetRun(ctx, targetRunID); runErr == nil && isTerminalRunStatus(target.Status) {
			return "", fmt.Errorf("replacement session %d ended as %s before answering", targetRunID, target.Status)
		}
		if err := waitForRoutedAnswerPoll(ctx); err != nil {
			return "", err
		}
	}
}

func waitForRoutedAnswerPoll(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

// askTaskOrchestrator is the worker-to-owner half of the side-channel. The
// question is persisted as a comment and durable inbox event, then the worker
// waits for the orchestrator's next LLM activation to return its answer.
func (e *NativeEngine) askTaskOrchestrator(ctx context.Context, task db.Task, orchestratorID, workerRunID int32, question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("question is required")
	}
	rid := workerRunID
	comment, err := e.q.CreateComment(ctx, db.Comment{
		TaskID: task.ID, AuthorType: "agent", CommentType: "ask_owner", Content: question, RunID: &rid,
	})
	if err != nil {
		return "", fmt.Errorf("record worker question: %w", err)
	}
	e.broadcastForTask(ctx, task.ID, "comment_created", comment)
	payload, _ := json.Marshal(db.NewSessionMessage(question))
	event, err := e.q.EnqueueRoutedEvent(ctx, task.ID, workerRunID, orchestratorID, db.RunEventTypeSessionMessage, string(payload), fmt.Sprintf("session-message:%d:%d:%d", workerRunID, orchestratorID, time.Now().UnixNano()))
	if err != nil {
		return "", fmt.Errorf("queue routed task-owner message: %w", err)
	}
	for {
		answer, findErr := e.q.FindAnswerForMessage(ctx, event.ID)
		if findErr == nil {
			return answer.Payload, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// askCEO starts a task-scoped CEO consultation. The consultation receives the
// normal task bootstrap and the caller's message as a routed inbound event;
// no serialized task snapshot is passed through the orchestrator tool call.
func (e *NativeEngine) askCEO(ctx context.Context, orchestrator db.Run, task db.Task, message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", fmt.Errorf("message is required")
	}
	ceo, err := e.findAgentForRole(ctx, task.CompanyID, "CEO")
	if err != nil {
		return "", fmt.Errorf("resolve CEO: %w", err)
	}
	parentID, rootID := orchestrator.ID, orchestrator.ID
	consultation, err := e.q.CreateRun(ctx, db.Run{
		TaskID: task.ID, AgentID: ceo.ID, Kind: db.RunKindCEOConsultation,
		Title:       "CEO consultation",
		ParentRunID: &parentID, RootRunID: &rootID, Status: "running", StartedAt: time.Now(),
	})
	if err != nil {
		return "", fmt.Errorf("create CEO consultation: %w", err)
	}
	payload, _ := json.Marshal(db.NewSessionMessage(message))
	inbound, err := e.q.EnqueueRoutedEvent(ctx, task.ID, orchestrator.ID, consultation.ID, db.RunEventTypeSessionMessage, string(payload), fmt.Sprintf("ceo-consultation:%d:%d", orchestrator.ID, consultation.ID))
	if err != nil {
		_ = e.q.UpdateRunLog(ctx, consultation.ID, err.Error(), "failed")
		return "", fmt.Errorf("queue CEO consultation message: %w", err)
	}
	consultationTask := task
	consultationTask.AgentID = &ceo.ID
	go e.executeSession(context.Background(), consultationTask, sessionModeImplement, &parentSession{
		parentRunID: orchestrator.ID, rootRunID: rootID, rootTaskID: task.ID,
	}, nil, sessionOptions{IncludeTaskContext: true, SkipTaskLock: true, PrecreatedRun: &consultation, Consultation: true})
	return fmt.Sprintf(`{"consultation_run_id":%d,"message_id":%d,"status":"started"}`, consultation.ID, inbound.ID), nil
}

func (e *NativeEngine) answerRoutedMessage(ctx context.Context, run db.Run, messageID int64, answer string) (string, error) {
	if strings.TrimSpace(answer) == "" {
		return "", fmt.Errorf("answer is required")
	}
	event, err := e.q.AnswerPendingMessage(ctx, run.ID, messageID, answer, fmt.Sprintf("session-answer:%d", messageID))
	if err != nil {
		return "", err
	}
	return event.Payload, nil
}

func (e *NativeEngine) orchestratorStop(ctx context.Context, orchestratorRunID, sessionID int32, reason string) (string, error) {
	if _, err := e.orchestratorSessionRun(ctx, orchestratorRunID, sessionID); err != nil {
		return "", err
	}
	_ = e.q.SetRunStopCause(ctx, sessionID, "orchestrator")
	e.StopRun(ctx, sessionID)
	return fmt.Sprintf("session %d stop requested: %s", sessionID, reason), nil
}

func (e *NativeEngine) orchestratorRunNew(ctx context.Context, task db.Task, orchestratorRunID int32, source *int32, agentName, title, prompt string) (string, error) {
	var sourceRun db.Run
	launchTask := task
	if source != nil {
		var err error
		sourceRun, err = e.orchestratorSessionRun(ctx, orchestratorRunID, *source)
		if err != nil {
			return "", err
		}
		launchTask, err = e.q.GetTask(ctx, sourceRun.TaskID)
		if err != nil {
			return "", err
		}
		if launchTask.RunID != nil && *launchTask.RunID != orchestratorRunID {
			return "", fmt.Errorf("task %d is locked by run %d, not orchestrator session %d", launchTask.ID, *launchTask.RunID, orchestratorRunID)
		}
		if sourceRun.Recovery.RecoveryAttempts >= 3 {
			return "", fmt.Errorf("session %d reached the automatic recovery limit", sourceRun.ID)
		}
		if err := e.q.IncrementRunRecoveryAttempts(ctx, sourceRun.ID); err != nil {
			return "", err
		}
		_ = e.q.SetRunStopCause(ctx, sourceRun.ID, "orchestrator")
		e.StopRun(ctx, sourceRun.ID)
		if err := e.waitForOrchestratorForkStop(ctx, sourceRun.ID); err != nil {
			return "", err
		}
		var taskErr error
		launchTask, taskErr = e.q.GetTask(ctx, sourceRun.TaskID)
		if taskErr != nil {
			return "", taskErr
		}
		if launchTask.RunID != nil && *launchTask.RunID != orchestratorRunID {
			return "", fmt.Errorf("task %d is locked by run %d, not orchestrator session %d", launchTask.ID, *launchTask.RunID, orchestratorRunID)
		}
	}
	agentName = strings.TrimSpace(agentName)
	title = strings.TrimSpace(title)
	prompt = strings.TrimSpace(prompt)
	if agentName == "" || title == "" || prompt == "" {
		return "", fmt.Errorf("agent_name, title, and prompt are required")
	}
	if len([]rune(title)) > 160 {
		return "", fmt.Errorf("title must be 160 characters or fewer")
	}
	if strings.EqualFold(agentName, "worker") {
		return e.orchestratorRunNewWorker(ctx, launchTask, orchestratorRunID, title, prompt, source != nil)
	}
	selectedAgent, err := e.findAgentForRole(ctx, launchTask.CompanyID, agentName)
	if err != nil {
		return "", fmt.Errorf("load target agent %q: %w", agentName, err)
	}
	if selectedAgent.CompanyID != launchTask.CompanyID {
		return "", fmt.Errorf("agent %d does not belong to task company", selectedAgent.ID)
	}
	precreated, err := e.createOrchestratorChildRun(ctx, launchTask, orchestratorRunID, selectedAgent, title)
	if err != nil {
		return "", fmt.Errorf("create worker session: %w", err)
	}
	e.startOrchestratorChildSession(launchTask, selectedAgent, precreated, prompt)
	if source != nil {
		return fmt.Sprintf("replacement child session %d queued for task %d with agent %s", precreated.ID, launchTask.ID, selectedAgent.Name), nil
	}
	return fmt.Sprintf("new child session %d queued for task %d with agent %s", precreated.ID, launchTask.ID, selectedAgent.Name), nil
}

func (e *NativeEngine) orchestratorRunNewWorker(ctx context.Context, task db.Task, orchestratorRunID int32, title, prompt string, replacement bool) (string, error) {
	if task.AgentID == nil {
		return "", fmt.Errorf("worker session requires an assigned task agent")
	}
	parent, err := e.q.GetRun(ctx, orchestratorRunID)
	if err != nil {
		return "", fmt.Errorf("load orchestrator session: %w", err)
	}
	children, err := e.q.ListChildRuns(ctx, orchestratorRunID)
	if err != nil {
		return "", err
	}
	active := 0
	for _, child := range children {
		if child.Kind == db.RunKindHelperWorker && (child.Status == "running" || child.Status == "waiting") {
			active++
		}
	}
	if active >= maxActiveHelperWorkers {
		return "", fmt.Errorf("orchestrator session %d already has %d active helper workers", orchestratorRunID, active)
	}
	backingAgent, err := e.q.GetAgent(ctx, *task.AgentID)
	if err != nil {
		return "", fmt.Errorf("load worker runtime agent: %w", err)
	}
	provider, model, err := e.resolveHelperWorkerModel(ctx, e.ownerUserIDForCompany(ctx, task.CompanyID))
	if err != nil {
		return "", fmt.Errorf("helper worker model unavailable: %w", err)
	}
	rootTask, err := e.q.GetRootTask(ctx, task.ID)
	if err != nil {
		rootTask = task
	}
	company, err := e.q.GetCompany(ctx, task.CompanyID)
	if err != nil {
		return "", err
	}
	manager := filesystem.NewManager(loadSettings().BasePath)
	parentWorkspace := strings.TrimSpace(parent.WorkspacePath)
	if parentWorkspace == "" {
		parentWorkspace = manager.GetTaskWorktreePath(company, rootTask)
	}
	artifactDir := manager.Paths().TaskArtifactsDir(company.ShortName, rootTask.ID)
	parentID, rootID := orchestratorRunID, orchestratorRunID
	if parent.RootRunID != nil {
		rootID = *parent.RootRunID
	}
	worker, err := e.q.CreateRun(ctx, db.Run{
		TaskID: task.ID, AgentID: backingAgent.ID, Kind: db.RunKindHelperWorker, Title: title,
		ParentRunID: &parentID, RootRunID: &rootID, Status: "running", StartedAt: time.Now(),
	})
	if err != nil {
		return "", fmt.Errorf("create worker session: %w", err)
	}
	workerDir := manager.Paths().RunWorkspaceDir(company.ShortName, rootTask.ID, worker.ID)
	if err := e.q.UpdateRunWorkspacePath(ctx, worker.ID, workerDir); err != nil {
		e.failRun(ctx, worker.ID, fmt.Sprintf("persist worker workspace: %v", err))
		return "", err
	}
	worker.WorkspacePath = workerDir
	workerTask := task
	workerTask.AgentID = &backingAgent.ID
	workerParent := &parentSession{parentRunID: orchestratorRunID, rootRunID: rootID, rootTaskID: rootTask.ID}
	go e.executeSession(context.Background(), workerTask, sessionModeImplement, workerParent, nil, sessionOptions{
		Instruction: prompt, IncludeTaskContext: true, SkipTaskLock: true, PrecreatedRun: &worker,
		Worker: true, WorkerWorkspace: workerDir, WorkerReadOnlyDirs: []string{parentWorkspace, artifactDir},
		WorkerProvider: provider, WorkerModel: model,
	})
	verb := "new"
	if replacement {
		verb = "replacement"
	}
	return fmt.Sprintf("%s Worker session %d queued for task %d with title %q and model %s", verb, worker.ID, task.ID, title, model), nil
}

func (e *NativeEngine) createOrchestratorChildRun(ctx context.Context, task db.Task, orchestratorRunID int32, agent db.Agent, title string) (db.Run, error) {
	parentID, rootID := orchestratorRunID, orchestratorRunID
	return e.q.CreateRun(ctx, db.Run{
		TaskID: task.ID, AgentID: agent.ID, Kind: db.RunKindAgentSession, Title: title, Status: "running", StartedAt: time.Now(),
		ParentRunID: &parentID, RootRunID: &rootID,
	})
}

func (e *NativeEngine) startOrchestratorChildSession(task db.Task, agent db.Agent, run db.Run, prompt string) {
	sessionTask := task
	sessionTask.AgentID = &agent.ID
	parentID, rootID := run.ID, run.ID
	if run.ParentRunID != nil {
		parentID = *run.ParentRunID
	}
	if run.RootRunID != nil {
		rootID = *run.RootRunID
	}
	workerParent := &parentSession{
		parentRunID: parentID,
		rootRunID:   rootID,
		rootTaskID:  task.ID,
	}
	go e.executeSession(context.Background(), sessionTask, sessionModeImplement, workerParent, nil, sessionOptions{
		Instruction:        prompt,
		IncludeTaskContext: true,
		SkipTaskLock:       true,
		PrecreatedRun:      &run,
	})
}

func (e *NativeEngine) waitForOrchestratorForkStop(ctx context.Context, runID int32) error {
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		run, err := e.q.GetRun(waitCtx, runID)
		if err != nil {
			return err
		}
		if isTerminalRunStatus(run.Status) || run.Status == db.RunStatusPaused {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("session %d did not stop before fork timeout", runID)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (e *NativeEngine) orchestratorFork(ctx context.Context, orchestratorRunID, sessionID int32, messageID int64) (string, error) {
	if messageID <= 0 {
		return "", fmt.Errorf("fork_message_id must be positive")
	}
	if _, err := e.orchestratorSessionRun(ctx, orchestratorRunID, sessionID); err != nil {
		return "", err
	}
	source, err := e.q.GetRun(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if source.Status == "running" || source.Status == "waiting" {
		_ = e.q.SetRunStopCause(ctx, sessionID, "orchestrator")
		e.StopRun(ctx, sessionID)
		if err := e.waitForOrchestratorForkStop(ctx, sessionID); err != nil {
			return "", err
		}
		source, err = e.q.GetRun(ctx, sessionID)
		if err != nil {
			return "", err
		}
	}
	if !isTerminalRunStatus(source.Status) && source.Status != db.RunStatusPaused {
		return "", fmt.Errorf("session %d is not at a forkable boundary (status %s)", sessionID, source.Status)
	}
	forkTask, err := e.q.GetTask(ctx, source.TaskID)
	if err != nil {
		return "", err
	}
	if forkTask.RunID != nil {
		// The orchestrator owns the task lock for its whole worker tree. A
		// source session may also own it in legacy/non-orchestrated flows, but
		// a fork requested by the orchestrator must not reject its own lock.
		if *forkTask.RunID != source.ID && *forkTask.RunID != orchestratorRunID {
			return "", fmt.Errorf("task %d is locked by run %d, not source session %d", forkTask.ID, *forkTask.RunID, source.ID)
		}
		if *forkTask.RunID == source.ID {
			if err := e.q.UnlockTaskRun(ctx, forkTask.ID); err != nil {
				return "", fmt.Errorf("unlock source task: %w", err)
			}
		}
	}
	history, safeMessageID, err := aicli.LoadSafeMessageHistoryAtOrBefore(source.LogFilePath, messageID)
	if err != nil {
		return "", err
	}
	rootTask, err := e.q.GetRootTask(ctx, source.TaskID)
	if err != nil {
		rootTask = forkTask
	}
	company, err := e.q.GetCompany(ctx, rootTask.CompanyID)
	if err != nil {
		return "", err
	}
	manager := filesystem.NewManager(loadSettings().BasePath)
	sourceWorkspace := strings.TrimSpace(source.WorkspacePath)
	if sourceWorkspace == "" {
		// Runs created before durable workspace persistence use the task's
		// canonical worktree. Keep this fallback for safe upgrades.
		sourceWorkspace = manager.GetTaskWorktreePath(company, rootTask)
	}
	parentID, rootID := orchestratorRunID, orchestratorRunID
	title := source.Title
	if strings.TrimSpace(title) == "" {
		title = "Forked session"
	}
	newRun, err := e.q.CreateRun(ctx, db.Run{
		TaskID: source.TaskID, AgentID: source.AgentID, Kind: db.RunKindAgentSession, Title: title, ParentRunID: &parentID, RootRunID: &rootID,
		Status: "running", StartedAt: time.Now(),
	})
	if err != nil {
		return "", fmt.Errorf("create forked session: %w", err)
	}
	forkWorkspace := manager.Paths().RunWorkspaceDir(company.ShortName, rootTask.ID, newRun.ID)
	if err := copyWorkspace(sourceWorkspace, forkWorkspace); err != nil {
		message := fmt.Sprintf("copy source workspace for fork: %v", err)
		e.failRun(ctx, newRun.ID, message)
		return "", fmt.Errorf("fork session %d: %s", newRun.ID, message)
	}
	if err := e.q.UpdateRunWorkspacePath(ctx, newRun.ID, forkWorkspace); err != nil {
		message := fmt.Sprintf("persist fork workspace: %v", err)
		e.failRun(ctx, newRun.ID, message)
		return "", fmt.Errorf("fork session %d: %s", newRun.ID, message)
	}
	newRun.WorkspacePath = forkWorkspace
	forkTask.AgentID = &source.AgentID
	// The orchestrator keeps the task lock for the whole worker tree. A forked
	// child is therefore an auxiliary session like run_new_session: it must not
	// race the orchestrator for the root task lock or clear that lock on exit.
	options := sessionOptions{
		SeedHistory:        history,
		IncludeTaskContext: true,
		SkipTaskLock:       true,
		PrecreatedRun:      &newRun,
	}
	go e.executeSession(context.Background(), forkTask, sessionModeImplement, nil, nil, options)
	return fmt.Sprintf("forked session %d from session %d at safe message %d", newRun.ID, sessionID, safeMessageID), nil
}

func orchestratorFingerprint(s []tools.ManagedSessionSummary) string {
	return orchestratorFingerprintWithTask(s, "", false)
}

func orchestratorFingerprintWithTask(s []tools.ManagedSessionSummary, taskStatus string, humanPending bool) string {
	state := struct {
		Sessions     []tools.ManagedSessionSummary `json:"sessions"`
		TaskStatus   string                        `json:"task_status"`
		HumanPending bool                          `json:"human_pending"`
	}{Sessions: s, TaskStatus: taskStatus, HumanPending: humanPending}
	b, _ := json.Marshal(state)
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:])
}

func isTerminalTaskStatus(s string) bool {
	return s == "done" || s == "in-review"
}

// in-review is a worker handoff state: the orchestrator may still need to
// launch the next stage. Only done means the entire task execution is over.
func isOrchestratorTaskComplete(s string) bool {
	return s == db.TaskStatusDone
}
func allWorkerSessionsTerminal(s []tools.ManagedSessionSummary) bool {
	if len(s) == 0 {
		return false
	}
	for _, v := range s {
		if !isTerminalRunStatus(v.LifecycleStatus) {
			return false
		}
	}
	return true
}
