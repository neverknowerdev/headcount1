package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/logging"
)

const maxActiveHelperWorkers = 4

func (e *NativeEngine) buildWorkerSessionTools(ctx context.Context, task db.Task, run db.Run, agent db.Agent, provider db.LLMProvider, model, workspace string, readOnlyDirs []string, logger *logging.ProxyLogger) *sessionToolState {
	state := &sessionToolState{}
	state.registry = tools.NewWorkerRegistry(workspace, readOnlyDirs, tools.WorkerCallbacks{
		ReportStatus: func(statusCtx context.Context, status string, messageID int64) error {
			if err := e.q.RecordRunStatusReport(statusCtx, run.ID, status, messageID); err != nil {
				return err
			}
			if run.ParentRunID == nil {
				return nil
			}
			payload, _ := json.Marshal(map[string]interface{}{"schema_version": 1, "worker_run_id": run.ID, "status": status, "message_id": messageID})
			_, err := e.q.EnqueueRoutedEvent(statusCtx, task.ID, run.ID, *run.ParentRunID, db.RunEventTypeStatusReport, string(payload), fmt.Sprintf("worker-status:%d:%d", run.ID, messageID))
			return err
		},
		FinishWork: func(finishCtx context.Context, result tools.FinishWorkResult) (string, error) {
			state.workerFinished = true
			status := db.TaskStatusDone
			if result.Status == tools.FinishWorkStatusBlocked {
				status = db.TaskStatusBlocked
			}
			if result.Status == tools.FinishWorkStatusFailed {
				status = db.TaskStatusBlocked
			}
			state.finishResult = tools.FinishTaskResult{Status: status, FinishStatus: result.Summary, ResultDetails: result.Details}
			if err := e.q.UpdateRunResult(finishCtx, run.ID, result.Summary, result.Details); err != nil {
				return "", err
			}
			if run.ParentRunID != nil {
				payload, _ := json.Marshal(db.WorkerFinishedMessage{SchemaVersion: 1, WorkerRunID: run.ID, Status: string(result.Status), Summary: result.Summary, Details: result.Details})
				if _, err := e.q.EnqueueRoutedEvent(finishCtx, task.ID, run.ID, *run.ParentRunID, db.RunEventTypeWorkerFinished, string(payload), fmt.Sprintf("worker-finished:%d", run.ID)); err != nil {
					return "", err
				}
			}
			if logger != nil {
				logger.LogInfo("Helper worker finished: " + string(result.Status) + " — " + result.Summary)
			}
			return fmt.Sprintf("Worker result recorded as %s.", string(result.Status)), nil
		},
	})
	return state
}

func (e *NativeEngine) prepareWorkerEnvironment(ctx context.Context, task *db.Task, run db.Run, options sessionOptions) (sessionEnvironment, db.Run, error) {
	var environment sessionEnvironment
	company, err := e.q.GetCompany(ctx, task.CompanyID)
	if err != nil {
		return environment, run, fmt.Errorf("failed to get worker company: %w", err)
	}
	rootTask, err := e.q.GetRootTask(ctx, task.ID)
	if err != nil {
		rootTask = *task
	}
	environment.company = company
	environment.rootTask = rootTask
	environment.rootTaskID = rootTask.ID
	environment.rootRunID = run.ID
	if run.RootRunID != nil {
		environment.rootRunID = *run.RootRunID
	}
	environment.provider = options.WorkerProvider
	environment.model = options.WorkerModel
	environment.workspacePath = options.WorkerWorkspace
	environment.readOnlyDirs = append([]string(nil), options.WorkerReadOnlyDirs...)
	settings := loadSettings()
	manager := filesystem.NewManager(settings.BasePath)
	environment.artifactDir = manager.Paths().TaskArtifactsDir(company.ShortName, rootTask.ID)
	if err := os.MkdirAll(environment.workspacePath, 0o700); err != nil {
		return environment, run, fmt.Errorf("create worker workspace: %w", err)
	}
	logger, logErr := logging.NewSessionLoggerWithHub(settings.BasePath, company.ShortName, rootTask.ID, environment.rootRunID, run.ID, e.hub.ForCompany(task.CompanyID), e.q)
	if logErr == nil {
		environment.logger = logger
		environment.cleanups = append(environment.cleanups, func() { _ = logger.Close() })
		_ = e.q.UpdateRunLogFilePath(ctx, run.ID, logger.FilePath())
	}
	// Cleanup is registered last so all terminal paths, including recovery
	// cleanup, remove the unique temporary writable directory.
	environment.cleanups = append(environment.cleanups, func() { _ = os.RemoveAll(environment.workspacePath) })
	return environment, run, nil
}

func (e *NativeEngine) runWorker(ctx context.Context, parent db.Run, task db.Task, prompt string) (string, error) {
	agent, err := e.q.GetAgent(ctx, parent.AgentID)
	if err != nil {
		return "", err
	}
	if !agent.CanUseWorkers {
		return "", fmt.Errorf("agent %q is not allowed to use helper workers", agent.Name)
	}
	children, err := e.q.ListChildRuns(ctx, parent.ID)
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
		return "", fmt.Errorf("parent run %d already has %d active helper workers", parent.ID, active)
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
	settings := loadSettings()
	manager := filesystem.NewManager(settings.BasePath)
	parentWorkspace := manager.GetTaskWorktreePath(company, rootTask)
	artifactDir := manager.Paths().TaskArtifactsDir(company.ShortName, rootTask.ID)
	workerDir, err := os.MkdirTemp("", "headcount1-helper-worker-")
	if err != nil {
		return "", err
	}
	parentID, rootID := parent.ID, parent.ID
	if parent.RootRunID != nil {
		rootID = *parent.RootRunID
	}
	worker, err := e.q.CreateRun(ctx, db.Run{TaskID: task.ID, AgentID: parent.AgentID, Kind: db.RunKindHelperWorker, ParentRunID: &parentID, RootRunID: &rootID, Status: "running", StartedAt: time.Now()})
	if err != nil {
		_ = os.RemoveAll(workerDir)
		return "", err
	}
	workerTask := task
	workerTask.AgentID = &parent.AgentID
	workerParent := &parentSession{parentRunID: parent.ID, rootRunID: rootID, rootTaskID: rootTask.ID}
	go e.executeSession(context.Background(), workerTask, "implement", workerParent, nil, sessionOptions{
		Instruction: prompt, IncludeTaskContext: true, SkipTaskLock: true, PrecreatedRun: &worker,
		Worker: true, WorkerPrompt: prompt, WorkerWorkspace: workerDir,
		WorkerReadOnlyDirs: []string{parentWorkspace, artifactDir}, WorkerProvider: provider, WorkerModel: model,
	})
	return fmt.Sprintf("helper worker run %d started with model %s", worker.ID, model), nil
}

func (e *NativeEngine) listHelperWorkers(ctx context.Context, parentID int32) ([]tools.WorkerSummary, error) {
	children, err := e.q.ListChildRuns(ctx, parentID)
	if err != nil {
		return nil, err
	}
	out := make([]tools.WorkerSummary, 0)
	for _, child := range children {
		if child.Kind != db.RunKindHelperWorker {
			continue
		}
		item := tools.WorkerSummary{ID: child.ID, Status: child.Status, LatestStatus: child.LatestReportedStatus, StartedAt: child.StartedAt.Format(time.RFC3339), Summary: child.ResultDescription}
		if child.EndedAt != nil {
			item.EndedAt = child.EndedAt.Format(time.RFC3339)
		}
		out = append(out, item)
	}
	return out, nil
}

func (e *NativeEngine) getHelperWorkerInfo(ctx context.Context, parentID, workerID int32) (string, error) {
	run, err := e.q.GetRun(ctx, workerID)
	if err != nil {
		return "", err
	}
	if run.Kind != db.RunKindHelperWorker || run.ParentRunID == nil || *run.ParentRunID != parentID {
		return "", fmt.Errorf("worker %d is not owned by run %d", workerID, parentID)
	}
	result := map[string]interface{}{"worker_id": run.ID, "status": run.Status, "latest_status": run.LatestReportedStatus, "summary": run.ResultDescription, "details": run.ResultExplanation, "log_tail": boundedLogTail(run.LogContent, 20)}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func boundedLogTail(content string, max int) []string {
	lines := strings.Split(content, "\n")
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func (e *NativeEngine) stopHelperWorker(ctx context.Context, parentID, workerID int32, reason string) (string, error) {
	run, err := e.q.GetRun(ctx, workerID)
	if err != nil {
		return "", err
	}
	if run.Kind != db.RunKindHelperWorker || run.ParentRunID == nil || *run.ParentRunID != parentID {
		return "", fmt.Errorf("worker %d is not owned by run %d", workerID, parentID)
	}
	if run.Status == "completed" || run.Status == "failed" || run.Status == "canceled" {
		return fmt.Sprintf("worker %d is already %s", workerID, run.Status), nil
	}
	e.StopRun(ctx, workerID)
	_ = e.q.SetRunStopCause(ctx, workerID, reason)
	return fmt.Sprintf("worker %d stop requested", workerID), nil
}
