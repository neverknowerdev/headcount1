package engine

import (
	"context"
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/secrets"
	"gorm.io/gorm"
)

const statusReportFreshness = 10 * time.Minute

//go:embed prompts/task_orchestrator.md
var orchestratorPrompt string

// createTaskOrchestrator creates the task-owned root run before its worker
// starts. Worker and delegated runs then form a normal child tree beneath it.
func (e *NativeEngine) createTaskOrchestrator(ctx context.Context, task db.Task, agent db.Agent) (db.Run, db.LLMProvider, string, bool, bool) {
	uid := e.ownerUserIDForCompany(ctx, task.CompanyID)
	setting, err := e.q.GetDefaultModelSetting(ctx, uid, db.PurposeTaskOrchestrator)
	if err != nil || (setting.ProviderID == nil && setting.ModelGroupID == nil) {
		return db.Run{}, db.LLMProvider{}, "", false, false
	}
	baseProvider, baseModel, err := resolveProvider(ctx, e.q, agent)
	if err != nil {
		return db.Run{}, db.LLMProvider{}, "", false, false
	}
	provider, model := e.resolvePurposeModel(ctx, uid, db.PurposeTaskOrchestrator, baseProvider, baseModel)
	if provider.ID == 0 || strings.TrimSpace(model) == "" {
		return db.Run{}, db.LLMProvider{}, "", false, false
	}
	if existing, err := e.q.GetOrchestratorRun(ctx, task.ID); err == nil && existing.ID != 0 && (existing.Status == "running" || existing.Status == "waiting") {
		return existing, provider, model, true, false
	}
	orchestrator := db.Run{
		TaskID: task.ID, AgentID: agent.ID, Status: "running", StartedAt: time.Now(),
		Name: fmt.Sprintf("%s-orchestrator", task.RefKey),
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
	go e.runOrchestrator(orchestrator, task, provider, model)
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
	fmt.Fprintf(&b, "Task type: %s\nTask status: %s\nPriority: %s\n", valueOrUnavailable(task.TaskType), valueOrUnavailable(task.Status), valueOrUnavailable(task.Priority))
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
	b.WriteString("\nStart execution by selecting the most appropriate available agent and calling run_new_session with a concrete prompt. The orchestrator owns coordination; workers own implementation and finish_task.")
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
		agent, err := e.q.GetAgent(ctx, *task.AgentID)
		if err != nil {
			continue
		}
		baseProvider, baseModel, err := resolveProvider(ctx, e.q, agent)
		if err != nil {
			continue
		}
		provider, model := e.resolvePurposeModel(ctx, e.ownerUserIDForCompany(ctx, task.CompanyID), db.PurposeTaskOrchestrator, baseProvider, baseModel)
		if provider.ID == 0 || model == "" {
			continue
		}
		go e.runOrchestrator(orch, task, provider, model)
	}
}

func (e *NativeEngine) runOrchestrator(orchestrator db.Run, task db.Task, provider db.LLMProvider, model string) {
	ctx := context.Background()
	logger, err := logging.NewSessionLoggerWithHub(loadSettings().BasePath, task.Company.ShortName, task.ID, orchestrator.ID, orchestrator.ID, e.hub.ForCompany(task.CompanyID), e.q)
	if err != nil {
		_ = e.q.UpdateRunLog(ctx, orchestrator.ID, err.Error(), "failed")
		return
	}
	defer logger.Close()
	defer e.q.UnlockTaskRun(context.Background(), task.ID)
	_ = e.q.UpdateRunLogFilePath(ctx, orchestrator.ID, logger.FilePath())
	questionBroker := newOrchestratorQuestionBroker()
	actualBroker, loaded := e.orchestratorQuestionChans.LoadOrStore(orchestrator.ID, questionBroker)
	if loaded {
		questionBroker = actualBroker.(*orchestratorQuestionBroker)
	}
	defer func() {
		e.orchestratorQuestionChans.Delete(orchestrator.ID)
		questionBroker.close(fmt.Errorf("orchestrator session %d ended before answering the worker", orchestrator.ID))
	}()
	systemPrompt, promptErr := e.buildOrchestratorSystemPrompt(ctx, task)
	if promptErr != nil {
		_ = e.q.UpdateRunLog(ctx, orchestrator.ID, promptErr.Error(), "failed")
		return
	}

	apiKey, _ := secrets.Default().Decrypt(provider.ApiKeyEncrypted)
	client := aicli.NewClient(provider.BaseUrl, apiKey, model)
	callbacks := tools.OrchestratorCallbacks{
		GetSessionList: func(c context.Context) ([]tools.ManagedSessionSummary, error) {
			return e.orchestratorSessions(c, orchestrator.ID)
		},
		GetSession: func(c context.Context, id int32) (tools.ManagedSessionDetails, error) {
			return e.orchestratorSessionDetails(c, task, orchestrator.ID, id)
		},
		AskAgent: func(c context.Context, id int32, question string) (string, error) {
			return e.orchestratorAskSession(c, task, orchestrator.ID, id, question)
		},
		RunNewSession: func(c context.Context, source *int32, agentName, prompt string) (string, error) {
			return e.orchestratorRunNew(c, task, orchestrator.ID, source, agentName, prompt)
		},
		StopSession: func(c context.Context, id int32, reason string) (string, error) {
			return e.orchestratorStop(c, orchestrator.ID, id, reason)
		},
		ForkSession: func(c context.Context, sessionID int32, messageID int64) (string, error) {
			return e.orchestratorFork(c, orchestrator.ID, sessionID, messageID)
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
		question, _ := questionBroker.receive()
		fingerprint := orchestratorFingerprint(sessions)
		taskNow, _ := e.q.GetTask(ctx, task.ID)
		if !first && fingerprint == lastFingerprint && len(events) == 0 && question == nil {
			if isTerminalTaskStatus(taskNow.Status) && allWorkerSessionsTerminal(sessions) {
				_ = e.q.UpdateRunLog(ctx, orchestrator.ID, "worker execution is terminal", "completed")
				return
			}
			_ = e.q.SetRunWaitState(ctx, orchestrator.ID, "waiting for worker lifecycle event")
			time.Sleep(2 * time.Second)
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
			message += "\nDurable lifecycle events since the last activation:\n" + string(eventJSON)
		}
		if question != nil {
			message += fmt.Sprintf("\n\nA worker is waiting for your answer. Respond directly to this worker question in your final text; do not leave it unanswered. Worker run %d asks:\n%s", question.workerRunID, question.question)
		}
		answer, runErr := ai.RunWithMessages(ctx, systemPrompt, []aicli.Message{{Role: "user", Content: message}})
		if question != nil {
			result := sessionQuestionResult{answer: strings.TrimSpace(answer), err: runErr}
			select {
			case question.result <- result:
			default:
			}
		}
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
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
		_ = e.q.SetRunWaitState(ctx, orchestrator.ID, "waiting for worker lifecycle event")
	}
}

func (e *NativeEngine) orchestratorSessions(ctx context.Context, orchestratorRunID int32) ([]tools.ManagedSessionSummary, error) {
	runs, err := e.q.ListOrchestratorSessions(ctx, orchestratorRunID)
	if err != nil {
		return nil, err
	}
	out := make([]tools.ManagedSessionSummary, 0, len(runs))
	for _, r := range runs {
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

func (e *NativeEngine) orchestratorSessionLastRunStatus(ctx context.Context, task db.Task, orchestratorRunID, id int32) (tools.ManagedSessionStatusReport, error) {
	r, err := e.orchestratorSessionRun(ctx, orchestratorRunID, id)
	if err != nil {
		return tools.ManagedSessionStatusReport{}, err
	}
	report, reportErr := e.q.GetLatestRunStatusReport(ctx, id)
	if reportErr != nil && !errors.Is(reportErr, gorm.ErrRecordNotFound) {
		return tools.ManagedSessionStatusReport{}, reportErr
	}
	result := tools.ManagedSessionStatusReport{ID: r.ID, Name: r.Name, TaskID: r.TaskID, AgentID: r.AgentID, AgentName: r.Agent.Name}
	if reportErr == nil {
		result.LastReportedStatus = report.Status
		result.LastReportedAt = report.ReportedAt.Format(time.RFC3339Nano)
		result.LastReportedMessageID = report.MessageID
	}
	stale := isStatusReportStale(report, reportErr == nil, time.Now())
	result.StatusReportStale = stale
	if stale && !isTerminalRunStatus(r.Status) {
		requested, requestErr := e.requestWorkerStatus(ctx, task, orchestratorRunID, id)
		if requestErr != nil {
			return tools.ManagedSessionStatusReport{}, requestErr
		}
		result.StatusRefreshRequested = requested
	}
	return result, nil
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
	if latest.LastReportedAt != "" {
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
		TaskID:            r.TaskID,
		AgentID:           r.AgentID,
		AgentName:         r.Agent.Name,
		LifecycleStatus:   r.Status,
		LastMessageTime:   last,
		WaitReason:        r.Recovery.WaitReason,
		RecoveryAttempts:  r.Recovery.RecoveryAttempts,
		StopCause:         r.Recovery.StopCause,
		ResultDescription: r.ResultDescription,
		Error:             r.LogContent,
	}
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

// orchestratorAskSession synchronously exchanges one isolated question/answer
// turn with another managed session. The worker's normal conversation is
// paused at its next safe boundary; provider/tool errors and timeouts are
// returned so the orchestrator agent receives them as the tool result.
func (e *NativeEngine) orchestratorAskSession(ctx context.Context, task db.Task, orchestratorID, sessionID int32, question string) (string, error) {
	_, err := e.orchestratorSessionRun(ctx, orchestratorID, sessionID)
	if err != nil {
		return "", err
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("question is required")
	}
	channelValue, ok := e.sessionQuestionChans.Load(sessionID)
	if !ok {
		return "", fmt.Errorf("session %d is not actively processing an LLM turn", sessionID)
	}
	questionBroker, ok := channelValue.(*sessionQuestionBroker)
	if !ok {
		return "", fmt.Errorf("session %d question broker is invalid", sessionID)
	}
	questionCtx, cancel := context.WithTimeout(ctx, orchestratorQuestionTimeout)
	defer cancel()
	request := &sessionQuestionRequest{question: question, ctx: questionCtx, result: make(chan sessionQuestionResult, 1)}
	// Keep a visible audit trail, but target the worker session rather than
	// attributing the question to the orchestrator run. Record it before
	// delivery so an audit failure cannot leave an untracked in-flight request.
	rid := sessionID
	comment, err := e.q.CreateComment(ctx, db.Comment{TaskID: task.ID, AuthorType: "system", CommentType: "orchestrator_question", Content: question, RunID: &rid})
	if err != nil {
		return "", err
	}
	e.broadcastForTask(ctx, task.ID, "comment_created", comment)
	if err := questionBroker.submit(questionCtx, request); err != nil {
		return "", fmt.Errorf("orchestrator question delivery failed for session %d: %w", sessionID, err)
	}
	select {
	case result := <-request.result:
		if result.err != nil {
			return "", result.err
		}
		return result.answer, nil
	case <-questionCtx.Done():
		return "", fmt.Errorf("orchestrator question timed out waiting for session %d response: %w", sessionID, questionCtx.Err())
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
	value, ok := e.orchestratorQuestionChans.Load(orchestratorID)
	if !ok {
		return "", fmt.Errorf("orchestrator session %d is not active", orchestratorID)
	}
	broker, ok := value.(*orchestratorQuestionBroker)
	if !ok {
		return "", fmt.Errorf("orchestrator session %d question broker is invalid", orchestratorID)
	}
	questionCtx, cancel := context.WithTimeout(ctx, orchestratorQuestionTimeout)
	defer cancel()
	rid := workerRunID
	comment, err := e.q.CreateComment(ctx, db.Comment{
		TaskID: task.ID, AuthorType: "agent", CommentType: "ask_owner", Content: question, RunID: &rid,
	})
	if err != nil {
		return "", fmt.Errorf("record worker question: %w", err)
	}
	e.broadcastForTask(ctx, task.ID, "comment_created", comment)
	payload, _ := json.Marshal(map[string]interface{}{
		"worker_run_id": workerRunID,
		"question":      question,
	})
	if err := e.q.EnqueueRunEvent(ctx, db.RunEvent{
		TaskID: task.ID, RunID: workerRunID, EventType: db.RunEventTypeWorkerQuestion,
		Payload: string(payload), DedupeKey: fmt.Sprintf("worker-question:%d:%d:%d", orchestratorID, workerRunID, time.Now().UnixNano()),
	}); err != nil {
		return "", fmt.Errorf("queue worker question: %w", err)
	}
	request := &orchestratorQuestionRequest{workerRunID: workerRunID, question: question, ctx: questionCtx, result: make(chan sessionQuestionResult, 1)}
	if err := broker.submit(questionCtx, request); err != nil {
		return "", fmt.Errorf("worker question delivery failed: %w", err)
	}
	select {
	case result := <-request.result:
		if result.err != nil {
			return "", result.err
		}
		if strings.TrimSpace(result.answer) == "" {
			return "", fmt.Errorf("orchestrator returned an empty answer")
		}
		return result.answer, nil
	case <-questionCtx.Done():
		return "", fmt.Errorf("timed out waiting for orchestrator %d response: %w", orchestratorID, questionCtx.Err())
	}
}

func (e *NativeEngine) orchestratorStop(ctx context.Context, orchestratorRunID, sessionID int32, reason string) (string, error) {
	if _, err := e.orchestratorSessionRun(ctx, orchestratorRunID, sessionID); err != nil {
		return "", err
	}
	_ = e.q.SetRunStopCause(ctx, sessionID, "orchestrator")
	e.StopRun(ctx, sessionID)
	return fmt.Sprintf("session %d stop requested: %s", sessionID, reason), nil
}

func (e *NativeEngine) orchestratorRunNew(ctx context.Context, task db.Task, orchestratorRunID int32, source *int32, agentName, prompt string) (string, error) {
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
	prompt = strings.TrimSpace(prompt)
	if agentName == "" || prompt == "" {
		return "", fmt.Errorf("agent_name and prompt are required")
	}
	selectedAgent, err := e.findAgentForRole(ctx, launchTask.CompanyID, agentName)
	if err != nil {
		return "", fmt.Errorf("load target agent %q: %w", agentName, err)
	}
	if selectedAgent.CompanyID != launchTask.CompanyID {
		return "", fmt.Errorf("agent %d does not belong to task company", selectedAgent.ID)
	}
	sessionTask := launchTask
	sessionTask.AgentID = &selectedAgent.ID
	parentID, rootID := orchestratorRunID, orchestratorRunID
	precreated, err := e.q.CreateRun(context.Background(), db.Run{
		TaskID: sessionTask.ID, AgentID: selectedAgent.ID, Status: "running", StartedAt: time.Now(),
		ParentRunID: &parentID, RootRunID: &rootID,
	})
	if err != nil {
		return "", fmt.Errorf("create worker session: %w", err)
	}
	// A worker is a child owned by the sidecar, so it never claims or unlocks
	// the root task lock. Its ask_task_owner callback is wired to the
	// orchestrator's durable question inbox.
	workerParent := &parentSession{
		parentRunID: orchestratorRunID,
		rootRunID:   orchestratorRunID,
		rootTaskID:  task.ID,
		depth:       1,
		askOwner: func(questionCtx context.Context, question string) (string, error) {
			return e.askTaskOrchestrator(questionCtx, task, orchestratorRunID, precreated.ID, question)
		},
	}
	go e.executeSession(context.Background(), sessionTask, "implement", workerParent, nil, sessionOptions{
		Instruction:        prompt,
		IncludeTaskContext: true,
		SkipTaskLock:       true,
		PrecreatedRun:      &precreated,
	})
	if source != nil {
		return fmt.Sprintf("replacement child session %d queued for task %d with agent %s", precreated.ID, launchTask.ID, selectedAgent.Name), nil
	}
	return fmt.Sprintf("new child session %d queued for task %d with agent %s", precreated.ID, launchTask.ID, selectedAgent.Name), nil
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
		if *forkTask.RunID != source.ID {
			return "", fmt.Errorf("task %d is locked by run %d, not source session %d", forkTask.ID, *forkTask.RunID, source.ID)
		}
		if err := e.q.UnlockTaskRun(ctx, forkTask.ID); err != nil {
			return "", fmt.Errorf("unlock source task: %w", err)
		}
	}
	history, safeMessageID, err := aicli.LoadSafeMessageHistoryAtOrBefore(source.LogFilePath, messageID)
	if err != nil {
		return "", err
	}
	parentID, rootID := orchestratorRunID, orchestratorRunID
	newRun, err := e.q.CreateRun(ctx, db.Run{
		TaskID: source.TaskID, AgentID: source.AgentID, ParentRunID: &parentID, RootRunID: &rootID,
		Status: "running", StartedAt: time.Now(),
	})
	if err != nil {
		return "", fmt.Errorf("create forked session: %w", err)
	}
	forkTask.AgentID = &source.AgentID
	go e.executeSession(context.Background(), forkTask, "implement", nil, nil, sessionOptions{
		SeedHistory: history, IncludeTaskContext: true, PrecreatedRun: &newRun,
	})
	return fmt.Sprintf("forked session %d from session %d at safe message %d", newRun.ID, sessionID, safeMessageID), nil
}

func orchestratorFingerprint(s []tools.ManagedSessionSummary) string {
	b, _ := json.Marshal(s)
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:])
}

func isTerminalTaskStatus(s string) bool {
	return s == "done" || s == "in-review" || s == "blocked" || s == "refinement"
}
func allWorkerSessionsTerminal(s []tools.ManagedSessionSummary) bool {
	if len(s) == 0 {
		return false
	}
	for _, v := range s {
		if v.LifecycleStatus == "running" || v.LifecycleStatus == "waiting" || v.LifecycleStatus == "interrupted" {
			return false
		}
	}
	return true
}

func isTerminalRunStatus(status string) bool {
	switch status {
	case "completed", "failed", "canceled", db.RunStatusRecoverableFailed, db.RunStatusStale, "interrupted":
		return true
	default:
		return false
	}
}

func isStatusReportStale(report db.RunStatusReport, hasReport bool, now time.Time) bool {
	return !hasReport || now.Sub(report.ReportedAt) > statusReportFreshness
}
