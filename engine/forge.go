package engine

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"

	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/eventhub"
)

type ForgeEngine struct {
	q   *db.Queries
	hub *eventhub.Hub
}

func NewForgeEngine(database *sql.DB, hub *eventhub.Hub) *ForgeEngine {
	return &ForgeEngine{
		q:   db.New(database),
		hub: hub,
	}
}

func (e *ForgeEngine) ProcessTask(ctx context.Context, taskID int32) error {
	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	switch task.Status {
	case "to-do":
		_, err = e.q.UpdateTaskStatus(ctx, db.UpdateTaskStatusParams{ID: task.ID, Status: "refinement"})
		if err != nil {
			return err
		}
		e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": "refinement"})
		go e.runForgeCLI(context.Background(), task, "plan")

	case "in-progress":
		go e.runForgeCLI(context.Background(), task, "implement")
	}

	return nil
}

func (e *ForgeEngine) runForgeCLI(ctx context.Context, task db.Task, mode string) {
	if !task.AgentID.Valid {
		return
	}

	agent, err := e.q.GetAgent(ctx, task.AgentID.Int32)
	if err != nil {
		return
	}

	run, err := e.q.CreateRun(ctx, db.CreateRunParams{
		TaskID:  task.ID,
		AgentID: agent.ID,
		Status:  "running",
	})
	if err != nil {
		return
	}
	e.hub.BroadcastEvent("run_started", run)

	comments, _ := e.q.ListCommentsByTask(ctx, task.ID)
	contextStr := fmt.Sprintf("Task: %s\nDescription: %s\nMode: %s\n\nComments:\n", task.Title, task.Description.String, mode)
	for _, c := range comments {
		contextStr += fmt.Sprintf("[%s]: %s\n", c.AuthorType, c.Content)
	}

	tmpDir := os.TempDir()
	promptFile := filepath.Join(tmpDir, fmt.Sprintf("task_%d_prompt.txt", task.ID))
	err = os.WriteFile(promptFile, []byte(contextStr), 0644)
	if err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to write prompt file: %v", err))
		return
	}
	defer os.Remove(promptFile)

	cmd := exec.CommandContext(ctx, "npx", "forgecode", "--prompt", promptFile)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SYSTEM_PROMPT=%s", agent.SystemPrompt),
		fmt.Sprintf("AGENT_MODEL=%s", agent.Model.String),
	)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		e.failRun(ctx, run.ID, err.Error())
		return
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		e.failRun(ctx, run.ID, err.Error())
		return
	}

	if err := cmd.Start(); err != nil {
		e.failRun(ctx, run.ID, err.Error())
		return
	}

	var fullLog strings.Builder

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			fullLog.WriteString(line + "\n")
			e.hub.BroadcastEvent("run_log", map[string]interface{}{"run_id": run.ID, "line": line})
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			fullLog.WriteString("ERROR: " + line + "\n")
			e.hub.BroadcastEvent("run_log", map[string]interface{}{"run_id": run.ID, "line": "ERROR: " + line})
		}
	}()

	err = cmd.Wait()

	now := sql.NullTime{Time: time.Now(), Valid: true}

	status := "completed"
	taskNextStatus := "in-review"

	if err != nil {
		status = "failed"
		taskNextStatus = "blocked"
	} else if strings.Contains(fullLog.String(), "NEED_USER_INPUT") || strings.Contains(fullLog.String(), "QUESTION_FOR_USER") {
		taskNextStatus = "blocked"
	}

	e.q.UpdateRunLog(ctx, db.UpdateRunLogParams{
		ID:         run.ID,
		LogContent: sql.NullString{String: fullLog.String(), Valid: true},
		Status:     status,
		EndedAt:    now,
	})

	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": status})

	e.q.UpdateTaskStatus(ctx, db.UpdateTaskStatusParams{
		ID:     task.ID,
		Status: taskNextStatus,
	})
	e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": taskNextStatus})
}

func (e *ForgeEngine) failRun(ctx context.Context, runID int32, errorMsg string) {
	e.q.UpdateRunLog(ctx, db.UpdateRunLogParams{
		ID:         runID,
		LogContent: sql.NullString{String: errorMsg, Valid: true},
		Status:     "failed",
	})
	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": runID, "status": "failed"})
}
