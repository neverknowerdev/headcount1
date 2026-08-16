package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/logging"
)

type sessionToolState struct {
	registry     *aicli.Registry
	taskFinished bool
	finishResult tools.FinishTaskResult
	gatewayAuth  runGatewayAuth
}

func (e *NativeEngine) buildSessionTools(
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
	depth int,
	logger *logging.ProxyLogger,
) *sessionToolState {
	state := &sessionToolState{
		registry:    tools.DefaultRegistry(workspacePath, readOnlyDirs...),
		gatewayAuth: runGatewayAuth{runID: run.ID},
	}

	state.registry.Register(tools.NewFinishTask(parent != nil, func(ctx context.Context, result tools.FinishTaskResult) error {
		current, err := e.q.GetTask(ctx, task.ID)
		if err != nil {
			return err
		}
		if result.Status != db.TaskStatusDone && result.Status != db.TaskStatusInReview && result.Status != db.TaskStatusBlocked {
			return fmt.Errorf("unsupported task status %q", result.Status)
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
	state.registry.Register(tools.NewAskArtifact(func(ctx context.Context, filename, question string) (string, error) {
		return e.askArtifact(ctx, run.ID, rootTaskID, provider, model, state.gatewayAuth, filename, question, logger)
	}))

	if depth < maxDelegationDepth {
		if subagents := decodeAgentNames(agent.Subagents); len(subagents) > 0 {
			pending := &pendingSubtasks{m: make(map[int32]*delegationState)}
			state.registry.Register(tools.NewCreateSubtask(e.makeCreateSubtaskFunc(task, run, logger, rootRunID, rootTaskID, workspacePath, depth, subagents, pending), subagents))
			state.registry.Register(tools.NewAnswerSubtaskQuestion(func(ctx context.Context, subtaskID int32, answer string) (string, error) {
				delegation, err := pending.take(subtaskID)
				if err != nil {
					return "", err
				}
				e.recordSubtaskQA(ctx, delegation.subtaskID, run.ID, "owner_answer", answer)
				e.logInfo(logger, fmt.Sprintf("Answered subtask #%d question", delegation.subtaskID))
				select {
				case delegation.answerCh <- answer:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				return e.waitForSubtaskEvent(ctx, delegation, rootTaskID, pending, logger, run)
			}))
		}
	}
	state.registry.Register(tools.NewCreateTask(func(ctx context.Context, params tools.CreateTaskParams) (string, error) {
		return e.createBoardTask(ctx, task, agent.ID, company, params)
	}))
	state.registry.Register(tools.NewAskHuman(func(ctx context.Context, question string) (string, error) {
		return e.askHuman(ctx, task.ID, run.ID, question)
	}))
	if parent != nil && parent.askOwner != nil {
		state.registry.Register(tools.NewAskTaskOwner(parent.askOwner))
	}
	state.registry.Register(tools.NewReportStatus(func(ctx context.Context, status string, messageID int64) error {
		if err := e.q.RecordRunStatusReport(ctx, run.ID, status, messageID); err != nil {
			return err
		}
		e.hub.BroadcastEventForCompany(task.CompanyID, "run_status", map[string]interface{}{"run_id": run.ID, "task_id": task.ID, "status": status})
		e.logInfo(logger, "Status: "+status)
		return nil
	}))
	return state
}
