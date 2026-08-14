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
)

//go:embed prompts/task_orchestrator.md
var orchestratorPrompt string

// startOrchestratorForWorker is called only after a worker root has acquired
// Task.RunID. The sidecar has its own Run row and never touches that lock.
func (e *NativeEngine) startOrchestratorForWorker(task db.Task, worker db.Run) {
	go e.orchestratorLoop(task, worker)
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
		if orch.SupervisedRunID == nil {
			continue
		}
		worker, err := e.q.GetRun(ctx, *orch.SupervisedRunID)
		if err != nil {
			continue
		}
		task, err := e.q.GetTask(ctx, worker.TaskID)
		if err != nil {
			continue
		}
		agent, err := e.q.GetAgent(ctx, worker.AgentID)
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
		go e.runOrchestrator(orch, task, worker, provider, model)
	}
}

func (e *NativeEngine) orchestratorLoop(task db.Task, worker db.Run) {
	ctx := context.Background()
	// A configured orchestrator setting is opt-in until every existing tenant
	// has selected a provider. This preserves existing tasks while making the
	// missing configuration visible in settings instead of silently falling
	// back to a worker model.
	uid := e.ownerUserIDForCompany(ctx, task.CompanyID)
	setting, err := e.q.GetDefaultModelSetting(ctx, uid, db.PurposeTaskOrchestrator)
	if err != nil || (setting.ProviderID == nil && setting.ModelGroupID == nil) {
		return
	}
	if existing, err := e.q.GetOrchestratorRun(ctx, worker.ID); err == nil && existing.ID != 0 {
		return
	}

	workerAgent, err := e.q.GetAgent(ctx, worker.AgentID)
	if err != nil {
		return
	}
	workerProvider, workerModel, err := resolveProvider(ctx, e.q, workerAgent)
	if err != nil {
		return
	}
	provider, model := e.resolvePurposeModel(ctx, uid, db.PurposeTaskOrchestrator, workerProvider, workerModel)
	if strings.TrimSpace(model) == "" || provider.ID == 0 {
		return
	}

	orch := db.Run{
		TaskID: task.ID, AgentID: worker.AgentID, Status: "running",
		SupervisedRunID: &worker.ID, StartedAt: time.Now(),
		Name: fmt.Sprintf("%s-ORCH-%d", task.RefKey, worker.ID),
	}
	created, err := e.q.CreateRun(ctx, orch)
	if err != nil {
		return
	}
	e.runOrchestrator(created, task, worker, provider, model)
}

func (e *NativeEngine) runOrchestrator(orchestrator db.Run, task db.Task, worker db.Run, provider db.LLMProvider, model string) {
	ctx := context.Background()
	logger, err := logging.NewSessionLoggerWithHub(loadSettings().BasePath, task.Company.ShortName, task.ID, orchestrator.ID, orchestrator.ID, e.hub.ForCompany(task.CompanyID), e.q)
	if err != nil {
		_ = e.q.UpdateRunLog(ctx, orchestrator.ID, err.Error(), "failed")
		return
	}
	defer logger.Close()
	_ = e.q.UpdateRunLogFilePath(ctx, orchestrator.ID, logger.FilePath())

	apiKey, _ := secrets.Default().Decrypt(provider.ApiKeyEncrypted)
	client := aicli.NewClient(provider.BaseUrl, apiKey, model)
	callbacks := tools.OrchestratorCallbacks{
		GetSessions: func(c context.Context) ([]tools.OrchestratorSession, error) {
			return e.orchestratorSessions(c, worker.ID)
		},
		GetSessionStatus: func(c context.Context, id int32) (tools.OrchestratorSession, error) {
			return e.orchestratorSessionStatus(c, worker.ID, id)
		},
		AskTaskOwner: func(c context.Context, id int32, question string) (string, error) {
			return e.orchestratorAskOwner(c, task, orchestrator.ID, worker.ID, id, question)
		},
		RunNewSession: func(c context.Context, source *int32, reason string) (string, error) {
			return e.orchestratorRunNew(c, task, worker.ID, source, reason)
		},
		StopSession: func(c context.Context, id int32, reason string) (string, error) {
			return e.orchestratorStop(c, worker.ID, id, reason)
		},
		ForkSession: func(c context.Context, sessionID int32, messageID int64) (string, error) {
			return e.orchestratorFork(c, worker.ID, sessionID, messageID)
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
		sessions, listErr := e.orchestratorSessions(ctx, worker.ID)
		if listErr != nil {
			_ = e.q.UpdateRunLog(ctx, orchestrator.ID, listErr.Error(), "failed")
			return
		}
		events, _ := e.q.ListPendingRunEvents(ctx, task.ID)
		fingerprint := orchestratorFingerprint(sessions)
		taskNow, _ := e.q.GetTask(ctx, task.ID)
		if !first && fingerprint == lastFingerprint && len(events) == 0 {
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
		if len(events) > 0 {
			ids := make([]int64, 0, len(events))
			for _, event := range events {
				ids = append(ids, event.ID)
			}
			_ = e.q.ConsumeRunEvents(ctx, ids)
		}
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
		_, runErr := ai.RunWithMessages(ctx, orchestratorPrompt, []aicli.Message{{Role: "user", Content: message}})
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			_ = e.q.UpdateRunLog(ctx, orchestrator.ID, runErr.Error(), "failed")
			return
		}
		_ = e.q.SetRunWaitState(ctx, orchestrator.ID, "waiting for worker lifecycle event")
	}
}

func (e *NativeEngine) orchestratorSessions(ctx context.Context, workerRootID int32) ([]tools.OrchestratorSession, error) {
	runs, err := e.q.ListSupervisedRuns(ctx, workerRootID)
	if err != nil {
		return nil, err
	}
	out := make([]tools.OrchestratorSession, 0, len(runs))
	for _, r := range runs {
		last := ""
		if r.LastMessageTime != nil {
			last = r.LastMessageTime.Format(time.RFC3339Nano)
		}
		out = append(out, tools.OrchestratorSession{ID: r.ID, Name: r.Name, TaskID: r.TaskID, Agent: r.Agent.Name, ParentRunID: r.ParentRunID, Status: r.Status, CurrentStatus: r.CurrentStatus, LastMessageTime: last, WaitReason: r.Recovery.WaitReason, RecoveryAttempts: r.Recovery.RecoveryAttempts, StopCause: r.Recovery.StopCause, ResultDescription: r.ResultDescription, Error: r.LogContent})
	}
	return out, nil
}

func (e *NativeEngine) orchestratorSessionStatus(ctx context.Context, workerRootID, id int32) (tools.OrchestratorSession, error) {
	r, err := e.q.GetRun(ctx, id)
	if err != nil {
		return tools.OrchestratorSession{}, err
	}
	valid := r.ID == workerRootID || r.RootRunID != nil && *r.RootRunID == workerRootID
	if r.SupervisedRunID != nil || !valid {
		return tools.OrchestratorSession{}, fmt.Errorf("session %d is outside the supervised worker tree", id)
	}
	list, err := e.orchestratorSessions(ctx, workerRootID)
	if err != nil {
		return tools.OrchestratorSession{}, err
	}
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return tools.OrchestratorSession{}, fmt.Errorf("session %d is not supervised", id)
}

func (e *NativeEngine) orchestratorAskOwner(ctx context.Context, task db.Task, orchestratorID, workerRootID, sessionID int32, question string) (string, error) {
	session, err := e.orchestratorSessionStatus(ctx, workerRootID, sessionID)
	if err != nil {
		return "", err
	}
	rid := orchestratorID
	comment, err := e.q.CreateComment(ctx, db.Comment{TaskID: task.ID, AuthorType: "system", CommentType: "orchestrator_question", Content: question, RunID: &rid})
	if err != nil {
		return "", err
	}
	e.broadcastForTask(ctx, task.ID, "comment_created", comment)
	return fmt.Sprintf("Current worker status: %s; current activity: %s. Question recorded for session %d; inspect its next lifecycle event for an answer.", session.Status, session.CurrentStatus, sessionID), nil
}

func (e *NativeEngine) orchestratorStop(ctx context.Context, workerRootID, sessionID int32, reason string) (string, error) {
	if _, err := e.orchestratorSessionStatus(ctx, workerRootID, sessionID); err != nil {
		return "", err
	}
	_ = e.q.SetRunStopCause(ctx, sessionID, "orchestrator")
	e.StopRun(ctx, sessionID)
	return fmt.Sprintf("session %d stop requested: %s", sessionID, reason), nil
}

func (e *NativeEngine) orchestratorRunNew(ctx context.Context, task db.Task, workerRootID int32, source *int32, reason string) (string, error) {
	attemptRunID := workerRootID
	if source != nil {
		if _, err := e.orchestratorSessionStatus(ctx, workerRootID, *source); err != nil {
			return "", err
		}
		attemptRunID = *source
	}
	current, err := e.q.GetRun(ctx, attemptRunID)
	if err != nil {
		return "", err
	}
	if current.Recovery.RecoveryAttempts >= 3 {
		return "", fmt.Errorf("session %d reached the automatic recovery limit", attemptRunID)
	}
	if err := e.q.IncrementRunRecoveryAttempts(ctx, attemptRunID); err != nil {
		return "", err
	}
	if source != nil {
		_ = e.q.SetRunStopCause(ctx, *source, "orchestrator")
		e.StopRun(ctx, *source)
	}
	if task.AgentID == nil {
		return "", fmt.Errorf("task has no assigned worker agent")
	}
	if task.RunID != nil && source != nil && *task.RunID != *source {
		return "", fmt.Errorf("task is already owned by another worker run")
	}
	if task.RunID != nil {
		_ = e.q.UnlockTaskRun(ctx, task.ID)
	}
	go e.run(context.Background(), task, "implement")
	return fmt.Sprintf("replacement worker queued for task %d: %s", task.ID, reason), nil
}

// orchestratorFork currently accepts a paused conversation position as the
// message identifier. The position is only usable when the engine persisted
// a safe checkpoint; an active or failed run without one is rejected instead
// of risking duplicated side effects.
func (e *NativeEngine) orchestratorFork(ctx context.Context, workerRootID, sessionID int32, messageID int64) (string, error) {
	if _, err := e.orchestratorSessionStatus(ctx, workerRootID, sessionID); err != nil {
		return "", err
	}
	source, err := e.q.GetRun(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if source.Status == "running" || source.Status == "waiting" {
		return "", fmt.Errorf("session %d must be stopped at a checkpoint before forking", sessionID)
	}
	// Checkpoint cursors identify durable replay boundaries, not arbitrary
	// message IDs. Refuse the operation until a dedicated fork checkpoint API
	// can persist a truncated JSONL history without risking duplicated tools.
	return "", fmt.Errorf("fork_session is unavailable for session %d: fork_message_id is not a durable checkpoint message ID", sessionID)
}

func orchestratorFingerprint(s []tools.OrchestratorSession) string {
	b, _ := json.Marshal(s)
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:])
}

func isTerminalTaskStatus(s string) bool {
	return s == "done" || s == "in-review" || s == "blocked" || s == "refinement"
}
func allWorkerSessionsTerminal(s []tools.OrchestratorSession) bool {
	if len(s) == 0 {
		return false
	}
	for _, v := range s {
		if v.Status == "running" || v.Status == "waiting" || v.Status == "interrupted" {
			return false
		}
	}
	return true
}
