package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/engine/workflow"
)

// buildTools assembles an executor's own tools: files, shell and web rooted
// at its working directory, the task tree's artifacts, and the two tools the
// workflow adds — checkpoint and finish_work. MCP servers and codegraph are
// added by the caller. An executor has no way to ask a human or to create
// tasks: what it cannot do itself, it reports.
func (s *executorSession) buildTools() *aicli.Registry {
	registry := tools.DefaultRegistry(s.workspace, s.readOnly...)
	s.registerArtifactTools(registry)
	s.checkpointTool = tools.NewCheckpoint(s.onCheckpoint)
	registry.Register(s.checkpointTool)
	registry.Register(tools.NewWorkReport(s.task.TaskType == models.TaskTypeReview, s.onFinishWork))
	return registry
}

func (s *executorSession) registerArtifactTools(registry *aicli.Registry) {
	q, task, run, rootID := s.e.q, s.task, s.run, s.root.ID
	registry.Register(tools.NewWriteArtifactFile(func(ctx context.Context, filename, content, description string) (string, error) {
		if filename != filepath.Base(filename) || filename == "." || filename == ".." {
			return "", fmt.Errorf("invalid artifact filename %q — use a plain filename without directories", filename)
		}
		if err := os.MkdirAll(s.artifactDir, 0o755); err != nil {
			return "", fmt.Errorf("could not create artifact directory: %w", err)
		}
		filePath := filepath.Join(s.artifactDir, filename)
		var existing *db.Artifact
		if artifacts, err := q.ListArtifactsByTaskTree(ctx, rootID); err == nil {
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
			if err := q.UpdateArtifactContent(ctx, existing.ID, content, run.ID); err != nil {
				return "", fmt.Errorf("could not update artifact: %w", err)
			}
			s.e.hub.BroadcastEventForCompany(task.CompanyID, "artifact_created", *existing)
			return fmt.Sprintf("Artifact %q updated (it already existed and was overwritten).", filename), nil
		}
		artifact, err := q.CreateArtifact(ctx, db.Artifact{TaskID: task.ID, RunID: &run.ID, Filename: filename, FilePath: filePath, Content: content, Description: description})
		if err != nil {
			return "", fmt.Errorf("could not save artifact: %w", err)
		}
		s.e.hub.BroadcastEventForCompany(task.CompanyID, "artifact_created", artifact)
		commentContent, _ := json.Marshal(map[string]string{"artifact_id": fmt.Sprintf("%d", artifact.ID), "filename": filename, "content": content})
		// The artifact belongs to the whole piece of work, so it is announced
		// where the human is looking: on the root task.
		if comment, err := q.CreateComment(ctx, db.Comment{TaskID: rootID, AuthorType: "system", CommentType: "artifact_created", Content: string(commentContent)}); err == nil {
			s.e.hub.BroadcastEventForCompany(task.CompanyID, "comment_created", comment)
		}
		return fmt.Sprintf("Artifact %q written.", filename), nil
	}))
	registry.Register(tools.NewListArtifacts(func(ctx context.Context) ([]tools.ArtifactInfo, error) {
		artifacts, err := q.ListArtifactsByTaskTree(ctx, rootID)
		if err != nil {
			return nil, err
		}
		infos := make([]tools.ArtifactInfo, 0, len(artifacts))
		for _, artifact := range artifacts {
			infos = append(infos, tools.ArtifactInfo{ID: artifact.ID, Filename: artifact.Filename, SizeBytes: len(artifact.Content),
				WrittenBy: fmt.Sprintf("run #%d", artifact.RunID), UpdatedAt: artifact.UpdatedAt.Format(time.RFC3339)})
		}
		return infos, nil
	}))
	registry.Register(tools.NewReadArtifact(func(ctx context.Context, filename string) (string, error) {
		artifacts, err := q.ListArtifactsByTaskTree(ctx, rootID)
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
}

// onCheckpoint records a checkpoint: the records it carries, minus what is
// already on file, and a journal entry that shows the task's progress to its
// parent and to the UI while the session is still running.
func (s *executorSession) onCheckpoint(ctx context.Context, input tools.CheckpointInput) (string, error) {
	s.checkpoints.recorded()
	s.turn.notable = false
	s.checkpointTool.SetProgressOnly(false)
	outcome, err := recordEntries(ctx, s.e.q, s.task, s.run, aicli.ToolCallMessageID(ctx), map[string][]tools.RecordInput{
		models.DecisionKindDecision:   input.Decisions,
		models.DecisionKindDeadEnd:    input.DeadEnds,
		models.DecisionKindAssumption: input.Assumptions,
	}, s.sameRecords)
	if err != nil {
		return "", err
	}
	progress := input.Progress
	if input.NextStep != "" {
		progress += " Next: " + input.NextStep
	}
	runID, agentID := s.run.ID, s.run.AgentID
	step, err := s.e.q.AppendTaskStep(ctx, db.TaskStep{
		TaskID: s.task.ID, RootTaskID: s.task.RootTaskID, Kind: models.StepCheckpoint, Phase: models.TaskPhaseExecute,
		AgentID: &agentID, RunID: &runID, Result: progress, CreatedAt: time.Now(),
	})
	if err != nil {
		return "", err
	}
	mirrorJournal(s.e.driver.basePath(), s.company.ShortName, s.task, []db.TaskStep{step}, outcome.saved)
	s.e.hub.BroadcastEventForCompany(s.task.CompanyID, "task_step", map[string]interface{}{"task_id": s.task.ID, "step": step})
	message := "Checkpoint recorded. " + outcome.message()
	return message + " Continue with the task.", nil
}

// onFinishWork stores the executor's report on its session and records what
// it decided since its last checkpoint. The report is written here, the
// moment it is made, so it survives anything that happens to the process
// before the task's next workflow step applies it.
func (s *executorSession) onFinishWork(ctx context.Context, report tools.WorkReport) (string, error) {
	outcome, err := recordEntries(ctx, s.e.q, s.task, s.run, aicli.ToolCallMessageID(ctx), map[string][]tools.RecordInput{
		models.DecisionKindDecision: report.Decisions,
		models.DecisionKindDeadEnd:  report.DeadEnds,
	}, s.sameRecords)
	if err != nil {
		return "", err
	}
	stored := workflow.Report{
		Status: report.Status, Summary: report.Summary, Details: report.Details,
		Evidence: report.Evidence, OpenQuestions: report.OpenQuestions, Verdict: report.Verdict,
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return "", err
	}
	if err := s.e.q.UpdateRunReport(ctx, s.run.ID, string(encoded), report.Summary, report.Details); err != nil {
		return "", fmt.Errorf("could not store the report: %w", err)
	}
	mirrorJournal(s.e.driver.basePath(), s.company.ShortName, s.task, nil, outcome.saved)
	s.report = &stored
	return "Report recorded as " + report.Status + ".", nil
}
