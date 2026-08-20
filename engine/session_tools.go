package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/logging"
)

type sessionToolState struct {
	registry       *aicli.Registry
	taskFinished   bool
	finishResult   tools.FinishTaskResult
	gatewayAuth    runGatewayAuth
	workerFinished bool
	consultation   bool
}

type delegatedTool struct {
	def      aicli.ToolDef
	registry *aicli.Registry
	name     string
}

func (t *delegatedTool) Def() aicli.ToolDef { return t.def }
func (t *delegatedTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.registry.Execute(ctx, t.name, args)
}

func (e *NativeEngine) buildSessionTools(
	buildCtx context.Context,
	task db.Task,
	run db.Run,
	agent db.Agent,
	company db.Company,
	parent *parentSession,
	provider db.LLMProvider,
	model string,
	workspacePath string,
	readOnlyDirs []string,
	artifactDir string,
	rootRunID int32,
	rootTaskID int32,
	logger *logging.ProxyLogger,
	mode string,
) *sessionToolState {
	state := &sessionToolState{
		registry:    tools.DefaultRegistry(workspacePath, readOnlyDirs...),
		gatewayAuth: runGatewayAuth{runID: run.ID},
	}
	// Forked orchestrator sessions are started from a pre-created Run and may
	// not carry an in-memory parentSession pointer. The persisted parent link
	// is the authoritative worker-tree boundary for finish_task semantics.
	delegated := parent != nil || run.ParentRunID != nil
	answerMessage := tools.NewAnswerMessage(func(ctx context.Context, messageID int64, answer string) (string, error) {
		return e.answerRoutedMessage(ctx, run, messageID, answer)
	})
	if pending, err := e.q.ListUnconsumedEventsForTarget(buildCtx, run.ID, db.RunEventTypeSessionMessage); err == nil && len(pending) > 0 {
		state.registry.Register(answerMessage)
	}

	state.registry.Register(tools.NewFinishTask(delegated, func(ctx context.Context, result tools.FinishTaskResult) error {
		if state.consultation {
			state.taskFinished = true
			state.finishResult = result
			return e.q.UpdateRunResult(ctx, run.ID, result.FinishStatus, result.ResultDetails)
		}
		if children, listErr := e.q.ListChildRuns(ctx, run.ID); listErr == nil {
			for _, child := range children {
				if child.Kind == db.RunKindHelperWorker && (child.Status == "running" || child.Status == "waiting") {
					return fmt.Errorf("cannot finish while helper worker %d is active; consume or stop it first", child.ID)
				}
			}
		}
		current, err := e.q.GetTask(ctx, task.ID)
		if err != nil {
			return err
		}
		if result.Status != db.TaskStatusDone && result.Status != db.TaskStatusInReview && result.Status != db.TaskStatusBlocked {
			return fmt.Errorf("unsupported task status %q", result.Status)
		}
		// A delegated session reports the outcome of its own work; it does not
		// own the shared task lifecycle. Updating the task here would move the
		// task to in-review/blocked as soon as the first child hands off, which
		// makes the passive orchestrator stop before it can schedule the next
		// stage. The task orchestrator is the only delegated boundary allowed to
		// make the final task transition through its own finish_task tool.
		if delegated {
			state.taskFinished = true
			state.finishResult = result
			return e.q.UpdateRunResult(ctx, run.ID, result.FinishStatus, result.ResultDetails)
		}
		if mode == "plan" {
			if result.Status == db.TaskStatusBlocked {
				current.Status = db.TaskStatusBlocked
			} else {
				if strings.TrimSpace(result.ResultDetails) == "" {
					return fmt.Errorf("refinement must provide result_details containing the updated specification")
				}
				current.RefinedDescription = result.ResultDetails
				current.Status = db.TaskStatusTodo
			}
			state.taskFinished = true
			state.finishResult = result
			if _, err := e.q.UpdateTask(ctx, current); err != nil {
				return err
			}
			if err := e.q.UpdateRunResult(ctx, run.ID, result.FinishStatus, result.ResultDetails); err != nil {
				return err
			}
			return nil
		}
		state.taskFinished = true
		state.finishResult = result
		previousStatus := current.Status
		current.Status = result.Status
		if _, err := e.q.UpdateTask(ctx, current); err != nil {
			return err
		}
		e.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", map[string]interface{}{"id": task.ID, "status": result.Status})
		if err := e.q.UpdateRunResult(ctx, run.ID, result.FinishStatus, result.ResultDetails); err != nil {
			fmt.Printf("Warning: failed to store run result: %v\n", err)
		}
		runID := run.ID
		content, _ := json.Marshal(map[string]string{"msg": result.FinishStatus, "from": previousStatus, "to": result.Status})
		comment, commentErr := e.q.CreateComment(ctx, db.Comment{TaskID: task.ID, AuthorType: "agent", CommentType: "task_done", Content: string(content), RunID: &runID})
		if commentErr == nil {
			e.hub.BroadcastEventForCompany(task.CompanyID, "comment_created", comment)
		}
		return nil
	}))

	state.registry.Register(tools.NewWriteArtifactFile(func(ctx context.Context, filename, content, description string) (string, error) {
		if filename != filepath.Base(filename) || filename == "." || filename == ".." {
			return "", fmt.Errorf("invalid artifact filename %q — use a plain filename without directories", filename)
		}
		if err := os.MkdirAll(artifactDir, 0o755); err != nil {
			return "", fmt.Errorf("could not create artifact directory: %w", err)
		}
		filePath := filepath.Join(artifactDir, filename)
		var existing *db.Artifact
		if artifacts, err := e.q.ListArtifactsByTaskTree(ctx, rootTaskID); err == nil {
			for i := len(artifacts) - 1; i >= 0; i-- {
				if artifacts[i].Filename == filename {
					existing = &artifacts[i]
					break
				}
			}
		}
		if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("could not write artifact file: %w", err)
		}
		if existing != nil {
			if err := e.q.UpdateArtifactContent(ctx, existing.ID, content, run.ID); err != nil {
				fmt.Printf("Warning: failed to update artifact in DB: %v\n", err)
			}
			e.hub.BroadcastEventForCompany(task.CompanyID, "artifact_created", *existing)
			if existing.RunID == run.ID {
				return fmt.Sprintf("Artifact %q updated.", filename), nil
			}
			return fmt.Sprintf("Artifact %q written — OVERWROTE an existing artifact originally written by run #%d. If that was not intended, use a different filename.", filename, existing.RunID), nil
		}
		artifact, err := e.q.CreateArtifact(ctx, db.Artifact{TaskID: task.ID, RunID: run.ID, Filename: filename, FilePath: filePath, Content: content, Description: description})
		if err != nil {
			fmt.Printf("Warning: failed to save artifact to DB: %v\n", err)
			return "", nil
		}
		e.hub.BroadcastEventForCompany(task.CompanyID, "artifact_created", artifact)
		commentContent, _ := json.Marshal(map[string]string{"artifact_id": fmt.Sprintf("%d", artifact.ID), "filename": filename, "content": content})
		if comment, commentErr := e.q.CreateComment(ctx, db.Comment{TaskID: task.ID, AuthorType: "system", CommentType: "artifact_created", Content: string(commentContent)}); commentErr == nil {
			e.hub.BroadcastEventForCompany(task.CompanyID, "comment_created", comment)
		}
		return fmt.Sprintf("Artifact %q written.", filename), nil
	}))

	state.registry.Register(tools.NewListArtifacts(func(ctx context.Context) ([]tools.ArtifactInfo, error) {
		artifacts, err := e.q.ListArtifactsByTaskTree(ctx, rootTaskID)
		if err != nil {
			return nil, err
		}
		infos := make([]tools.ArtifactInfo, 0, len(artifacts))
		for _, artifact := range artifacts {
			infos = append(infos, tools.ArtifactInfo{ID: artifact.ID, Filename: artifact.Filename, SizeBytes: len(artifact.Content), WrittenBy: fmt.Sprintf("run #%d", artifact.RunID), UpdatedAt: artifact.UpdatedAt.Format(time.RFC3339)})
		}
		return infos, nil
	}))
	state.registry.Register(tools.NewReadArtifact(func(ctx context.Context, filename string) (string, error) {
		artifacts, err := e.q.ListArtifactsByTaskTree(ctx, rootTaskID)
		if err != nil {
			return "", err
		}
		for i := len(artifacts) - 1; i >= 0; i-- {
			if artifacts[i].Filename == filename {
				return artifacts[i].Content, nil
			}
		}
		return "", fmt.Errorf("artifact %q not found — call list_artifacts to see what exists", filename)
	}))

	// Task hierarchy is a CEO capability. Keep the service-side role check in
	// the callback as an authorization boundary even if a malformed registry
	// exposes one of these tools.
	state.registry.Register(tools.NewCreateTask(func(ctx context.Context, params tools.CreateTaskParams) (string, error) {
		if !agentconfig.RoleMatches(agent.RoleKey, agent.Name, "CEO") {
			return "", fmt.Errorf("create_task is restricted to the CEO role")
		}
		return e.createBoardTask(ctx, task, agent.ID, company, params)
	}))
	state.registry.Register(tools.NewCreateSubtask(func(ctx context.Context, params tools.CreateSubtaskParams) (string, error) {
		if !agentconfig.RoleMatches(agent.RoleKey, agent.Name, "CEO") {
			return "", fmt.Errorf("create_subtask is restricted to the CEO role")
		}
		return e.createSubtask(ctx, task, params)
	}))
	state.registry.Register(tools.NewGetTask(func(ctx context.Context, reference string) (string, error) {
		if !agentconfig.RoleMatches(agent.RoleKey, agent.Name, "CEO") {
			return "", fmt.Errorf("get_task is restricted to the CEO role")
		}
		return e.getTaskOperationalView(ctx, task.CompanyID, reference)
	}))
	if agentCanUseWorkers(agent) {
		workerRegistry := tools.NewWorkerControlRegistry(tools.WorkerControlCallbacks{
			RunWorker: func(workerCtx context.Context, prompt string) (string, error) {
				return e.runWorker(workerCtx, run, task, prompt)
			},
			ListWorkers: func(workerCtx context.Context) ([]tools.WorkerSummary, error) {
				return e.listHelperWorkers(workerCtx, run.ID)
			},
			GetWorkerInfo: func(workerCtx context.Context, workerID int32) (string, error) {
				return e.getHelperWorkerInfo(workerCtx, run.ID, workerID)
			},
			StopWorker: func(workerCtx context.Context, workerID int32, reason string) (string, error) {
				return e.stopHelperWorker(workerCtx, run.ID, workerID, reason)
			},
		})
		for _, name := range workerRegistry.Names() {
			// Registry does not expose the implementation map; register through a
			// small adapter that dispatches back to the independently-built set.
			state.registry.Register(&delegatedTool{def: workerRegistry.DefsByName(name), registry: workerRegistry, name: name})
		}
	}
	if agentconfig.RoleMatches(agent.RoleKey, agent.Name, "CEO") {
		state.registry.Register(tools.NewAskHuman(func(ctx context.Context, question string) (string, error) {
			return e.askHuman(ctx, task.ID, run.ID, question)
		}))
	}
	if orchestrator, err := e.q.GetOrchestratorRun(buildCtx, task.ID); err == nil && orchestrator.ID > 0 && orchestrator.Status != "completed" && orchestrator.Status != "failed" && orchestrator.Status != "canceled" {
		state.registry.Register(tools.NewAskTaskOwner(func(messageCtx context.Context, question string) (string, error) {
			return e.askTaskOrchestrator(messageCtx, task, orchestrator.ID, run.ID, question)
		}))
	}
	state.registry.Register(tools.NewReportStatus(func(ctx context.Context, status string, messageID int64) error {
		if err := e.q.RecordRunStatusReport(ctx, run.ID, status, messageID); err != nil {
			return err
		}
		e.hub.BroadcastEventForCompany(task.CompanyID, "run_status", map[string]interface{}{"run_id": run.ID, "task_id": task.ID, "status": status})
		e.logInfo(logger, "Status: "+status)
		return nil
	}))
	// Persisted database agents do not store the built-in config's
	// AllowedTools list. Apply the canonical role contract at the runtime
	// boundary so a CEO/CTO/QA row cannot accidentally receive implementation
	// tools merely because the default registry contains them. Unknown custom
	// roles retain the legacy unrestricted registry until they opt into an
	// explicit capability policy.
	if allowed := roleToolNames(agent); len(allowed) > 0 {
		state.registry = state.registry.Filter(allowed)
	}
	return state
}
