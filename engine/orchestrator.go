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
	_ = e.q.UpdateRunLogFilePath(ctx, orchestrator.ID, logger.FilePath())

	apiKey, _ := secrets.Default().Decrypt(provider.ApiKeyEncrypted)
	client := aicli.NewClient(provider.BaseUrl, apiKey, model)
	callbacks := tools.OrchestratorCallbacks{
		GetSessions: func(c context.Context) ([]tools.OrchestratorSession, error) {
			return e.orchestratorSessions(c, orchestrator.ID)
		},
		GetSessionLastRunStatus: func(c context.Context, id int32) (tools.OrchestratorSessionLastRunStatus, error) {
			return e.orchestratorSessionLastRunStatus(c, task, orchestrator.ID, id)
		},
		AskTaskOwner: func(c context.Context, id int32, question string) (string, error) {
			return e.orchestratorAskOwner(c, task, orchestrator.ID, id, question)
		},
		RunNewSession: func(c context.Context, source *int32, reason string) (string, error) {
			return e.orchestratorRunNew(c, task, orchestrator.ID, source, reason)
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

func (e *NativeEngine) orchestratorSessions(ctx context.Context, orchestratorRunID int32) ([]tools.OrchestratorSession, error) {
	runs, err := e.q.ListOrchestratorSessions(ctx, orchestratorRunID)
	if err != nil {
		return nil, err
	}
	out := make([]tools.OrchestratorSession, 0, len(runs))
	for _, r := range runs {
		last := ""
		if r.LastMessageTime != nil {
			last = r.LastMessageTime.Format(time.RFC3339Nano)
		}
		out = append(out, tools.OrchestratorSession{ID: r.ID, Name: r.Name, TaskID: r.TaskID, Agent: r.Agent.Name, Status: r.Status, LastMessageTime: last, WaitReason: r.Recovery.WaitReason, RecoveryAttempts: r.Recovery.RecoveryAttempts, StopCause: r.Recovery.StopCause, ResultDescription: r.ResultDescription, Error: r.LogContent})
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

func (e *NativeEngine) orchestratorSessionLastRunStatus(ctx context.Context, task db.Task, orchestratorRunID, id int32) (tools.OrchestratorSessionLastRunStatus, error) {
	r, err := e.orchestratorSessionRun(ctx, orchestratorRunID, id)
	if err != nil {
		return tools.OrchestratorSessionLastRunStatus{}, err
	}
	report, reportErr := e.q.GetLatestRunStatusReport(ctx, id)
	if reportErr != nil && !errors.Is(reportErr, gorm.ErrRecordNotFound) {
		return tools.OrchestratorSessionLastRunStatus{}, reportErr
	}
	result := tools.OrchestratorSessionLastRunStatus{ID: r.ID, Name: r.Name, TaskID: r.TaskID, Agent: r.Agent.Name}
	if reportErr == nil {
		result.LastReportedStatus = report.Status
		result.LastReportedAt = report.ReportedAt.Format(time.RFC3339Nano)
	}
	stale := isStatusReportStale(report, reportErr == nil, time.Now())
	result.StatusReportStale = stale
	if stale && !isTerminalRunStatus(r.Status) {
		requested := r.StatusRefreshRequestedAt != nil && time.Since(*r.StatusRefreshRequestedAt) < statusReportFreshness
		if !requested {
			question := "Please report your current status now using report_status. Include the stage you are working on and any blocker."
			if _, askErr := e.orchestratorAskOwner(ctx, task, orchestratorRunID, id, question); askErr == nil {
				now := time.Now()
				if setErr := e.q.SetRunStatusRefreshRequestedAt(ctx, id, &now); setErr == nil {
					requested = true
				}
			}
		}
		result.StatusRefreshRequested = requested
	}
	return result, nil
}

func (e *NativeEngine) orchestratorAskOwner(ctx context.Context, task db.Task, orchestratorID, sessionID int32, question string) (string, error) {
	session, err := e.orchestratorSessionRun(ctx, orchestratorID, sessionID)
	if err != nil {
		return "", err
	}
	rid := orchestratorID
	comment, err := e.q.CreateComment(ctx, db.Comment{TaskID: task.ID, AuthorType: "system", CommentType: "orchestrator_question", Content: question, RunID: &rid})
	if err != nil {
		return "", err
	}
	e.broadcastForTask(ctx, task.ID, "comment_created", comment)
	return fmt.Sprintf("Current worker lifecycle status: %s. Question recorded for session %d; inspect its next lifecycle event for an answer.", session.Status, sessionID), nil
}

func (e *NativeEngine) orchestratorStop(ctx context.Context, orchestratorRunID, sessionID int32, reason string) (string, error) {
	if _, err := e.orchestratorSessionRun(ctx, orchestratorRunID, sessionID); err != nil {
		return "", err
	}
	_ = e.q.SetRunStopCause(ctx, sessionID, "orchestrator")
	e.StopRun(ctx, sessionID)
	return fmt.Sprintf("session %d stop requested: %s", sessionID, reason), nil
}

func (e *NativeEngine) orchestratorRunNew(ctx context.Context, task db.Task, orchestratorRunID int32, source *int32, reason string) (string, error) {
	attemptRunID := orchestratorRunID
	if source != nil {
		if _, err := e.orchestratorSessionRun(ctx, orchestratorRunID, *source); err != nil {
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
func (e *NativeEngine) orchestratorFork(ctx context.Context, orchestratorRunID, sessionID int32, messageID int64) (string, error) {
	if _, err := e.orchestratorSessionRun(ctx, orchestratorRunID, sessionID); err != nil {
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
