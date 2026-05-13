package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/eventhub"
	"gorm.io/gorm"
)

type OpenCodeEngine struct {
	q   *db.Queries
	hub *eventhub.Hub
}

func NewOpenCodeEngine(database *gorm.DB, hub *eventhub.Hub) *OpenCodeEngine {
	// Ensure opencode is installed
	err := exec.Command("opencode", "--version").Run()
	if err != nil {
		fmt.Println("opencode not found. Installing via npm...")
		installCmd := exec.Command("npm", "install", "-g", "opencode-ai")
		installCmd.Stdout = os.Stdout
		installCmd.Stderr = os.Stderr
		if err := installCmd.Run(); err != nil {
			fmt.Printf("Failed to install opencode: %v\n", err)
		} else {
			fmt.Println("opencode successfully installed.")
		}
	}

	// Start the OpenCode Server if it's not already running
	if _, err := http.Get("http://127.0.0.1:36000/ping"); err != nil {
		fmt.Println("Starting OpenCode server...")
		cmd := exec.Command("opencode", "serve", "--port", "36000")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Printf("Failed to start OpenCode server: %v\n", err)
		} else {
			// wait a bit to ensure it started
			for i := 0; i < 10; i++ {
				time.Sleep(500 * time.Millisecond)
				if _, err := http.Get("http://127.0.0.1:36000/session"); err == nil {
					fmt.Println("OpenCode server is ready.")
					break
				}
			}
		}
	} else {
		fmt.Println("OpenCode server is already running.")
	}

	return &OpenCodeEngine{
		q:   db.New(database),
		hub: hub,
	}
}

func (e *OpenCodeEngine) ProcessTask(ctx context.Context, taskID int32) error {
	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	switch task.Status {
	case "to-do":
		if task.TaskType == db.TaskTypeImplement {
			task.Status = "in-progress"
			_, err = e.q.UpdateTask(ctx, task)
			if err != nil {
				return err
			}
			e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": "in-progress"})
			go e.runOpenCode(context.Background(), task, "implement")
		} else {
			task.Status = "refinement"
			_, err = e.q.UpdateTask(ctx, task)
			if err != nil {
				return err
			}
			e.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": "refinement"})
			go e.runOpenCode(context.Background(), task, "plan")
		}

	case "in-progress":
		go e.runOpenCode(context.Background(), task, "implement")
	}

	return nil
}

func (e *OpenCodeEngine) failRun(ctx context.Context, runID int32, errorMsg string) {
	e.q.UpdateRunLog(ctx, runID, errorMsg, "failed")
	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": runID, "status": "failed"})
}

func (e *OpenCodeEngine) runOpenCode(ctx context.Context, task db.Task, mode string) {
	if task.AgentID == nil {
		return
	}

	agent, err := e.q.GetAgent(ctx, *task.AgentID)
	if err != nil {
		return
	}

	run, err := e.q.CreateRun(ctx, db.Run{
		TaskID:    task.ID,
		AgentID:   agent.ID,
		Status:    "running",
		StartedAt: time.Now(),
	})
	if err != nil {
		return
	}
	e.hub.BroadcastEvent("run_started", run)

	comments, _ := e.q.ListCommentsByTask(ctx, task.ID)
	attachments, _ := e.q.ListAttachmentsByTask(ctx, task.ID)

	// Use SystemPromptBuilder to inject contextStr into user request
	promptBuilder := NewSystemPromptBuilder(e.q)
	systemPromptContext := promptBuilder.Build(agent, task)

	contextStr := fmt.Sprintf("%s\nTask: %s\nDescription: %s\nMode: %s\n\n", systemPromptContext, task.Title, task.Description, mode)

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

	var fullLog strings.Builder
	logLine := func(line string) {
		fullLog.WriteString(line + "\n")
		e.hub.BroadcastEvent("run_log", map[string]interface{}{"run_id": run.ID, "line": line})
	}

	logLine("Connecting to OpenCode Server...")

	baseURL := "http://127.0.0.1:36000"

	// Create session
	sessionReqBody, _ := json.Marshal(map[string]interface{}{
		"title": fmt.Sprintf("Task %d: %s", task.ID, mode),
	})

	client := &http.Client{Timeout: 30 * time.Minute}
	sessionResp, err := client.Post(baseURL+"/session", "application/json", bytes.NewBuffer(sessionReqBody))
	if err != nil || sessionResp.StatusCode != 200 {
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to create OpenCode session: %v", err))
		if sessionResp != nil {
			sessionResp.Body.Close()
		}
		return
	}

	var sessionData struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(sessionResp.Body).Decode(&sessionData); err != nil {
		e.failRun(ctx, run.ID, "Failed to decode OpenCode session response")
		sessionResp.Body.Close()
		return
	}
	sessionResp.Body.Close()

	e.q.UpdateRunLog(ctx, run.ID, fullLog.String(), "running") // Force save log
	e.q.UpdateRunSession(ctx, run.ID, sessionData.ID)
	logLine(fmt.Sprintf("Created session %s", sessionData.ID))

	msgReqBody := map[string]interface{}{
		"agent": agent.Name,
		"parts": []map[string]interface{}{
			{
				"type": "text",
				"text": contextStr,
			},
		},
	}

	if agent.Model != "" {
		modelObj := map[string]interface{}{
			"modelID": agent.Model,
		}
		if agent.ProviderID != nil {
			provider, err := e.q.GetLLMProvider(ctx, *agent.ProviderID)
			if err == nil {
				providerID := provider.ProviderType
				if providerID == "" {
					providerID = provider.Name
				}
				modelObj["providerID"] = providerID
				logLine(fmt.Sprintf("Using provider: name=%s type=%s providerID=%s base_url=%s", provider.Name, provider.ProviderType, providerID, provider.BaseUrl))
			} else {
				logLine(fmt.Sprintf("Failed to get provider %d: %v", *agent.ProviderID, err))
			}
		} else {
			logLine("No provider ID set on agent")
		}
		msgReqBody["model"] = modelObj
	}

	msgReqBytes, _ := json.Marshal(msgReqBody)

	logLine(fmt.Sprintf("Request body: %s", string(msgReqBytes)))
	logLine(fmt.Sprintf("Sending message to OpenCode session using model %s and agent %s...", agent.Model, agent.Name))

	msgResp, err := client.Post(fmt.Sprintf("%s/session/%s/message", baseURL, sessionData.ID), "application/json", bytes.NewBuffer(msgReqBytes))

	status := "completed"

	if err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to send message: %v", err))
		return
	}
	defer msgResp.Body.Close()

	respBodyBytes, _ := io.ReadAll(msgResp.Body)
	logLine(fmt.Sprintf("Response status: %d", msgResp.StatusCode))
	if len(respBodyBytes) > 0 {
		logLine(fmt.Sprintf("Response body: %s", string(respBodyBytes)))
	} else {
		logLine("Response body: (empty)")
	}

	if msgResp.StatusCode != http.StatusOK {
		status = "failed"
		logLine(fmt.Sprintf("ERROR: OpenCode Server returned %d: %s", msgResp.StatusCode, string(respBodyBytes)))
	} else if len(respBodyBytes) == 0 {
		status = "failed"
		providerID := ""
		if agent.ProviderID != nil {
			if p, err := e.q.GetLLMProvider(ctx, *agent.ProviderID); err == nil {
				providerID = p.ProviderType
			}
		}
		logLine(fmt.Sprintf("ERROR: OpenCode returned empty response. Model '%s' may not exist on provider '%s'. Check agent configuration.", agent.Model, providerID))
	} else {
		var rawMsg map[string]interface{}
		agentResponse := ""

		if err := json.Unmarshal(respBodyBytes, &rawMsg); err == nil {
			if parts, ok := rawMsg["parts"].([]interface{}); ok {
				var text strings.Builder
				for _, p := range parts {
					if part, ok := p.(map[string]interface{}); ok {
						if t, ok := part["text"].(string); ok && t != "" {
							text.WriteString(t)
							text.WriteString("\n")
						}
					}
				}
				agentResponse = strings.TrimSpace(text.String())
			}
		}

		if agentResponse == "" {
			agentResponse = string(respBodyBytes)
		}

		if agentResponse != "" {
			logLine(agentResponse)

			comment, _ := e.q.CreateComment(ctx, db.Comment{
				TaskID:     task.ID,
				AuthorType: "agent",
				Content:    agentResponse,
			})
			e.hub.BroadcastEvent("comment_created", comment)
		} else {
			logLine("WARNING: OpenCode returned empty response")
		}
	}

	taskAgain, _ := e.q.GetTask(ctx, task.ID)
	if taskAgain.Status == task.Status && status == "completed" {
		logLine("Task status not updated by agent. Forcing update_task_status call...")

		forceMsgBody := map[string]interface{}{
			"agent": agent.Name,
			"parts": []map[string]interface{}{
				{
					"type": "text",
					"text": "Please use the update_task_status tool to set the task status to in-progress, blocked, or in-review as appropriate.",
				},
			},
		}

		if agent.Model != "" {
			modelObj := map[string]interface{}{
				"modelID": agent.Model,
			}
			if agent.ProviderID != nil {
				provider, err := e.q.GetLLMProvider(ctx, *agent.ProviderID)
				if err == nil {
					providerID := provider.ProviderType
					if providerID == "" {
						providerID = provider.Name
					}
					modelObj["providerID"] = providerID
				}
			}
			forceMsgBody["model"] = modelObj
		}

		forceMsgBytes, _ := json.Marshal(forceMsgBody)
		forceResp, err := client.Post(fmt.Sprintf("%s/session/%s/message", baseURL, sessionData.ID), "application/json", bytes.NewBuffer(forceMsgBytes))

		if err == nil {
			defer forceResp.Body.Close()
			forceRespBytes, _ := io.ReadAll(forceResp.Body)

			var rawForceMsg map[string]interface{}
			forceAgentResponse := ""

			if err := json.Unmarshal(forceRespBytes, &rawForceMsg); err == nil {
				if parts, ok := rawForceMsg["parts"].([]interface{}); ok {
					var text strings.Builder
					for _, p := range parts {
						if part, ok := p.(map[string]interface{}); ok {
							if t, ok := part["text"].(string); ok && t != "" {
								text.WriteString(t)
								text.WriteString("\n")
							}
						}
					}
					forceAgentResponse = strings.TrimSpace(text.String())
				}
			}

			if forceAgentResponse == "" {
				forceAgentResponse = string(forceRespBytes)
			}

			if forceAgentResponse != "" {
				logLine(forceAgentResponse)
				comment, _ := e.q.CreateComment(ctx, db.Comment{
					TaskID:     task.ID,
					AuthorType: "agent",
					Content:    forceAgentResponse,
				})
				e.hub.BroadcastEvent("comment_created", comment)
			}
		} else {
			logLine(fmt.Sprintf("Failed to send force message: %v", err))
		}
	}

	e.q.UpdateRunLog(ctx, run.ID, fullLog.String(), status)

	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": status})
}
