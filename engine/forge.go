package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"context"

	"agent-orchestrator/pkg/utils"

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
	attachments, _ := e.q.ListAttachmentsByTask(ctx, task.ID)

	contextStr := fmt.Sprintf("Task: %s\nDescription: %s\nMode: %s\n\n", task.Title, task.Description, mode)

	if len(attachments) > 0 {
		contextStr += "Attachments:\n"
		for _, a := range attachments {
			if strings.HasPrefix(a.MimeType, "image/") {
				contextStr += fmt.Sprintf("- %s (image, cannot be read by this model)\n", a.Filename)
			} else {
				contextStr += fmt.Sprintf("- %s\n", a.Filename)
			}
		}
		contextStr += "\n"
	}

	if len(comments) > 0 {
		contextStr += "Comments:\n"
		for _, c := range comments {
			contextStr += fmt.Sprintf("[%s]: %s\n", c.AuthorType, c.Content)
		}
	}

	if agent.ProviderID == nil {
		e.failRun(ctx, run.ID, "Agent has no provider configured")
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

	isAnthropic := provider.ProviderType == "anthropic"

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

	if isAnthropic {
		reqBody["max_tokens"] = 4096
	}

	reqBodyBytes, _ := json.Marshal(reqBody)

	targetURL := utils.BuildProviderURL(provider.BaseUrl, "/chat/completions")
	if isAnthropic {
		targetURL = utils.BuildProviderURL(provider.BaseUrl, "/messages")
	}

	logLine(fmt.Sprintf("Sending request to %s (provider: %s, model: %s)...", targetURL, provider.Name, agent.Model))

	req, err := http.NewRequest("POST", targetURL, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to create request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	if isAnthropic {
		req.Header.Set("x-api-key", provider.ApiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+provider.ApiKey)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to contact provider: %v", err))
		return
	}
	defer resp.Body.Close()

	respBodyBytes, _ := io.ReadAll(resp.Body)

	status := "completed"
	taskNextStatus := "in-progress"
	if mode == "implement" {
		taskNextStatus = "in-review"
	}

	if resp.StatusCode != http.StatusOK {
		status = "failed"
		taskNextStatus = "blocked"

		errMsg := parseErrorResponse(resp.StatusCode, respBodyBytes)
		logLine(fmt.Sprintf("ERROR: %s", errMsg))
	} else {
		agentResponse := extractResponseContent(respBodyBytes, isAnthropic)
		if agentResponse != "" {
			logLine(agentResponse)

			comment, _ := e.q.CreateComment(ctx, db.Comment{
				TaskID:     task.ID,
				AuthorType: "agent",
				Content:    agentResponse,
			})
			e.hub.BroadcastEvent("comment_created", comment)
		} else {
			logLine("WARNING: Provider returned empty response")
		}
	}

	e.q.UpdateRunLog(ctx, run.ID, fullLog.String(), status)

	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": status})

	task.Status = taskNextStatus
	e.q.UpdateTask(ctx, task)
	e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": taskNextStatus})
}

func parseErrorResponse(statusCode int, body []byte) string {
	bodyStr := string(body)

	if strings.Contains(strings.ToLower(bodyStr), "<html") {
		switch statusCode {
		case 404:
			return "Endpoint not found (404). Please check the provider Base URL."
		case 401, 403:
			return "Invalid API Key or unauthorized access."
		default:
			return fmt.Sprintf("Provider returned HTML error (status %d). Please check the provider URL and configuration.", statusCode)
		}
	}

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		return fmt.Sprintf("Provider error: %s", errResp.Error.Message)
	}

	var anthropicErr struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &anthropicErr) == nil && anthropicErr.Message != "" {
		return fmt.Sprintf("Provider error: %s", anthropicErr.Message)
	}

	return fmt.Sprintf("Provider returned status %d: %s", statusCode, truncate(bodyStr, 500))
}

func extractResponseContent(body []byte, isAnthropic bool) string {
	if isAnthropic {
		var resp struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(body, &resp) == nil && len(resp.Content) > 0 {
			var text strings.Builder
			for _, c := range resp.Content {
				if c.Type == "text" {
					text.WriteString(c.Text)
				}
			}
			return text.String()
		}
	}

	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &resp) == nil && len(resp.Choices) > 0 {
		return resp.Choices[0].Message.Content
	}

	return ""
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (e *ForgeEngine) failRun(ctx context.Context, runID int32, errorMsg string) {
	e.q.UpdateRunLog(ctx, runID, errorMsg, "failed")
	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": runID, "status": "failed"})
}
