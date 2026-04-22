package engine

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"context"
	"fmt"
		"strings"

	"gorm.io/gorm"

	"agent-orchestrator/db"
	"agent-orchestrator/eventhub"
)

type ForgeEngine struct {
	q   *db.Queries
	hub *eventhub.Hub
}

func NewForgeEngine(database *gorm.DB, hub *eventhub.Hub) *ForgeEngine {
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
		task.Status = "refinement"
		_, err = e.q.UpdateTask(ctx, task)
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
	if task.AgentID == nil {
		return
	}

	agent, err := e.q.GetAgent(ctx, *task.AgentID)
	if err != nil {
		return
	}

	run, err := e.q.CreateRun(ctx, db.Run{
		TaskID:  task.ID,
		AgentID: agent.ID,
		Status:  "running",
	})
	if err != nil {
		return
	}
	e.hub.BroadcastEvent("run_started", run)

	comments, _ := e.q.ListCommentsByTask(ctx, task.ID)
	contextStr := fmt.Sprintf("Task: %s\nDescription: %s\nMode: %s\n\nComments:\n", task.Title, task.Description, mode)
	for _, c := range comments {
		contextStr += fmt.Sprintf("[%s]: %s\n", c.AuthorType, c.Content)
	}

	if agent.ProviderID == nil {
		e.failRun(ctx, run.ID, "Agent has no provider")
		return
	}

	provider, err := e.q.GetLLMProvider(ctx, *agent.ProviderID)
	if err != nil {
		e.failRun(ctx, run.ID, "Failed to get provider")
		return
	}

	var fullLog strings.Builder
	logLine := func(line string) {
		fullLog.WriteString(line + "\n")
		e.hub.BroadcastEvent("run_log", map[string]interface{}{"run_id": run.ID, "line": line})
	}

	reqBody := map[string]interface{}{
		"model": agent.Model,
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": agent.SystemPrompt,
			},
			{
				"role":    "user",
				"content": contextStr,
			},
		},
	}

	reqBodyBytes, _ := json.Marshal(reqBody)
	logLine(fmt.Sprintf("Sending request to %s...", provider.BaseUrl))

	req, err := http.NewRequest("POST", provider.BaseUrl+"/v1/chat/completions", bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to create request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.ApiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to contact provider: %v", err))
		return
	}
	defer resp.Body.Close()

	respBodyBytes, _ := ioutil.ReadAll(resp.Body)

	status := "completed"
	taskNextStatus := "in-progress"
	if mode == "implement" {
		taskNextStatus = "in-review"
	}

	if resp.StatusCode != http.StatusOK {
		status = "failed"
		taskNextStatus = "blocked"
		logLine(fmt.Sprintf("ERROR: Provider returned status %d", resp.StatusCode))
		logLine(string(respBodyBytes))
	} else {
		var resPayload struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		json.Unmarshal(respBodyBytes, &resPayload)
		agentResponse := ""
		if len(resPayload.Choices) > 0 {
			agentResponse = resPayload.Choices[0].Message.Content
			logLine(agentResponse)

			// Add agent response as a comment
			comment, _ := e.q.CreateComment(ctx, db.Comment{
				TaskID:     task.ID,
				AuthorType: "agent",
				Content:    agentResponse,
			})
			e.hub.BroadcastEvent("comment_created", comment)
		}
	}

	e.q.UpdateRunLog(ctx, run.ID, fullLog.String(), status)

	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": status})

	task.Status = taskNextStatus
	e.q.UpdateTask(ctx, task)
	e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": taskNextStatus})
}

func (e *ForgeEngine) failRun(ctx context.Context, runID int32, errorMsg string) {
	e.q.UpdateRunLog(ctx, runID, errorMsg, "failed")
	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": runID, "status": "failed"})
}
