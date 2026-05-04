package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/eventhub"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type LogEntry struct {
	ID          string                 `json:"id"`
	Timestamp   string                 `json:"timestamp"`
	Type        string                 `json:"type"`
	Content     string                 `json:"content"`
	FullContent string                 `json:"fullContent,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

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
		installCmd.Stdout = nil
		installCmd.Stderr = nil
		if err := installCmd.Run(); err != nil {
			fmt.Printf("Failed to install opencode: %v\n", err)
		} else {
			fmt.Println("opencode successfully installed.")
		}
	}

	// Start the OpenCode Server if it's not already running
	if !isServerRunning("http://127.0.0.1:36000") {
		fmt.Println("Starting OpenCode server...")
		cmd := exec.Command("opencode", "serve", "--port", "36000")
		if err := cmd.Start(); err != nil {
			fmt.Printf("Failed to start OpenCode server: %v\n", err)
		} else {
			// Wait for it to become ready
			for i := 0; i < 10; i++ {
				time.Sleep(1 * time.Second)
				if isServerRunning("http://127.0.0.1:36000") {
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

func isServerRunning(baseURL string) bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/doc") // hitting the docs endpoint
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (e *OpenCodeEngine) ProcessTask(ctx context.Context, taskID int32) error {
	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	switch task.Status {
	case "to-do":
		if task.TaskType == "implement" {
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
	// Fetch existing run to get current log entries
	run, err := e.q.GetRun(ctx, runID)
	var logEntries []LogEntry
	if err == nil && run.LogContent != "" {
		json.Unmarshal([]byte(run.LogContent), &logEntries)
	}
	// Append error entry
	logEntries = append(logEntries, LogEntry{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Type:      "error",
		Content:   fmt.Sprintf("❌ Run failed: %s", errorMsg),
	})
	logJSON, _ := json.Marshal(logEntries)
	e.q.UpdateRunLog(ctx, runID, string(logJSON), "failed")
	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": runID, "status": "failed"})
}

func (e *OpenCodeEngine) ReRunTask(ctx context.Context, taskID int32, mode string) error {
	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}
	go e.runOpenCode(context.Background(), task, mode)
	return nil
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
		Mode:      mode,
		StartedAt: time.Now(),
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

	var logEntries []LogEntry
	logEntry := func(entryType, content string, fullContent string, metadata map[string]interface{}) {
		entry := LogEntry{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Type:      entryType,
			Content:   content,
		}
		if fullContent != "" && fullContent != content {
			entry.FullContent = fullContent
		}
		if metadata != nil {
			entry.Metadata = metadata
		}
		logEntries = append(logEntries, entry)
		e.hub.BroadcastEvent("run_log_entry", map[string]interface{}{
			"run_id": run.ID,
			"entry":  entry,
		})
		// Persist to DB after every entry so logs survive crashes/timeouts
		logJSON, _ := json.Marshal(logEntries)
		e.q.UpdateRunLog(ctx, run.ID, string(logJSON), "running")
	}

	logEntry("info", "Connecting to OpenCode Server...", "", nil)

	baseURL := "http://127.0.0.1:36000"

	// Create session
	sessionReqBody, _ := json.Marshal(map[string]interface{}{
		"title": fmt.Sprintf("Task %d: %s", task.ID, mode),
	})

	client := &http.Client{Timeout: 5 * time.Minute}
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

	// Update run with session id
	e.q.UpdateRunSession(ctx, run.ID, sessionData.ID)
	logEntry("info", fmt.Sprintf("Created session %s", sessionData.ID), "", nil)

	// Ensure the Provider auth is set correctly on the OpenCode server if needed.
	// Since we only know we need to send the model and the payload, let's prepare the message request.

	msgReqBody := map[string]interface{}{
		"parts": []map[string]interface{}{
			{
				"type": "text",
				"text": contextStr,
			},
		},
	}

	// Use SystemPromptBuilder
	promptBuilder := NewSystemPromptBuilder(e.q)
	systemPrompt := promptBuilder.Build(agent, task)
	if systemPrompt != "" {
		msgReqBody["system"] = systemPrompt
	}

	if agent.Model != "" {
		modelObj := map[string]interface{}{
			"modelID": agent.Model,
		}
		if agent.ProviderID != nil {
			provider, err := e.q.GetLLMProvider(ctx, *agent.ProviderID)
			if err == nil {
				// Use ProviderType as providerID (lowercase ID format like "opencode-go")
				// Fall back to Name if ProviderType is empty
				providerID := provider.ProviderType
				if providerID == "" {
					providerID = provider.Name
				}
				modelObj["providerID"] = providerID
				logEntry("info", fmt.Sprintf("Using provider: %s (%s)", provider.Name, providerID), "", map[string]interface{}{
					"provider":   provider.Name,
					"providerID": providerID,
					"base_url":   provider.BaseUrl,
				})
			} else {
				logEntry("error", fmt.Sprintf("Failed to get provider %d: %v", *agent.ProviderID, err), "", nil)
			}
		} else {
			logEntry("warning", "No provider ID set on agent", "", nil)
		}
		msgReqBody["model"] = modelObj
	}

	msgReqBytes, _ := json.Marshal(msgReqBody)

	// Log the model request with trimmed content
	logEntry("request", fmt.Sprintf("📤 Request to Model (%s)", agent.Model), string(msgReqBytes), map[string]interface{}{
		"model":      agent.Model,
		"provider":   agent.Model,
		"requestLen": len(msgReqBytes),
	})

	logEntry("info", fmt.Sprintf("⏳ Waiting for model %s to respond...", agent.Model), "", nil)

	msgResp, err := client.Post(fmt.Sprintf("%s/session/%s/message", baseURL, sessionData.ID), "application/json", bytes.NewBuffer(msgReqBytes))

	status := "completed"

	if err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to send message: %v", err))
		return
	}
	defer msgResp.Body.Close()

	respBodyBytes, _ := io.ReadAll(msgResp.Body)

	if msgResp.StatusCode != http.StatusOK {
		status = "failed"
		logEntry("error", fmt.Sprintf("❌ OpenCode Server returned %d", msgResp.StatusCode), string(respBodyBytes), map[string]interface{}{
			"statusCode": msgResp.StatusCode,
			"model":      agent.Model,
		})
	} else if len(respBodyBytes) == 0 {
		// Empty 200 response usually means invalid model/provider combination
		status = "failed"
		providerID := ""
		if agent.ProviderID != nil {
			if p, err := e.q.GetLLMProvider(ctx, *agent.ProviderID); err == nil {
				providerID = p.ProviderType
			}
		}
		logEntry("error", fmt.Sprintf("❌ Empty response from model '%s' on provider '%s'", agent.Model, providerID), "", map[string]interface{}{
			"model":    agent.Model,
			"provider": providerID,
		})
	} else {
		// OpenCode returns { info: Message, parts: Part[] }
		// Parse to extract tool calls and text responses separately
		var rawMsg map[string]interface{}
		agentResponse := ""
		var toolCalls []map[string]interface{}

		if err := json.Unmarshal(respBodyBytes, &rawMsg); err == nil {
			if parts, ok := rawMsg["parts"].([]interface{}); ok {
				var text strings.Builder
				for _, p := range parts {
					if part, ok := p.(map[string]interface{}); ok {
						partType, _ := part["type"].(string)

						switch partType {
						case "tool-invocation":
							// Extract tool call information
							toolName, _ := part["toolName"].(string)
							toolArgs, _ := part["args"].(map[string]interface{})
							toolResult, _ := part["result"].(string)
							
							toolCall := map[string]interface{}{
								"toolName": toolName,
								"args":     toolArgs,
							}
							if toolResult != "" {
								toolCall["resultSize"] = len(toolResult)
							}
							toolCalls = append(toolCalls, toolCall)

							// Log tool call as structured entry
							logEntry("tool_call", 
								fmt.Sprintf("🔧 Tool Call: %s", toolName),
								fmt.Sprintf("Args: %v\nResult: %s", toolArgs, toolResult),
								map[string]interface{}{
									"toolName":     toolName,
									"toolParams":   toolArgs,
									"responseSize": len(toolResult),
								})

						default:
							if t, ok := part["text"].(string); ok && t != "" {
								text.WriteString(t)
								text.WriteString("\n")
							}
						}
					}
				}
				agentResponse = strings.TrimSpace(text.String())
			}
		}

	// Log the model response
	logEntry("response", 
		fmt.Sprintf("📥 Response from Model (%d chars)", len(agentResponse)),
		agentResponse,
		map[string]interface{}{
			"model":       agent.Model,
			"responseLen": len(agentResponse),
			"toolCalls":   len(toolCalls),
		})

	// Fetch all session messages to get intermediate tool calls
	sessionMsgsResp, err := client.Get(fmt.Sprintf("%s/session/%s/message", baseURL, sessionData.ID))
	if err == nil {
		defer sessionMsgsResp.Body.Close()
		sessionMsgsBytes, _ := io.ReadAll(sessionMsgsResp.Body)
		
		var sessionMsgs []map[string]interface{}
		if err := json.Unmarshal(sessionMsgsBytes, &sessionMsgs); err == nil {
			for _, msg := range sessionMsgs {
				info, _ := msg["info"].(map[string]interface{})
				role, _ := info["role"].(string)
				if role != "assistant" {
					continue
				}
				
				parts, _ := msg["parts"].([]interface{})
				for _, p := range parts {
					part, ok := p.(map[string]interface{})
					if !ok {
						continue
					}
					partType, _ := part["type"].(string)
					
					switch partType {
					case "tool":
						toolName, _ := part["tool"].(string)
						state, _ := part["state"].(map[string]interface{})
						toolStatus, _ := state["status"].(string)
						input, _ := state["input"].(map[string]interface{})
						output, _ := state["output"].(string)
						title, _ := state["title"].(string)
						
						preview := output
						if len(preview) > 200 {
							preview = preview[:200] + "..."
						}
						
						displayName := toolName
						if title != "" {
							displayName = fmt.Sprintf("%s (%s)", toolName, title)
						}
						
						logEntry("tool_call",
							fmt.Sprintf("🔧 Tool: %s [%s]", displayName, toolStatus),
							preview,
							map[string]interface{}{
								"toolName":     toolName,
								"toolParams":   input,
								"responseSize": len(output),
								"status":       toolStatus,
							})
					}
				}
			}
		}
	}

		if agentResponse == "" {
			agentResponse = string(respBodyBytes)
		}

		if agentResponse != "" {
			comment, _ := e.q.CreateComment(ctx, db.Comment{
				TaskID:     task.ID,
				AuthorType: "agent",
				Content:    agentResponse,
			})
			e.hub.BroadcastEvent("comment_created", comment)
		} else {
			logEntry("warning", "⚠️ OpenCode returned empty response", "", nil)
		}
	}

	// Fetch task again to check if status was updated by tool
	taskAgain, _ := e.q.GetTask(ctx, task.ID)
	if taskAgain.Status == task.Status && status == "completed" {
		// LLM did not update status, force it to call update_task_status
		logEntry("info", "Task status not updated by agent. Forcing update_task_status call...", "", nil)

		forceMsgBody := map[string]interface{}{
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
		
		// Log force request
		logEntry("request", "📤 Forcing update_task_status call", string(forceMsgBytes), map[string]interface{}{
			"model":      agent.Model,
			"requestLen": len(forceMsgBytes),
		})

		forceResp, err := client.Post(fmt.Sprintf("%s/session/%s/message", baseURL, sessionData.ID), "application/json", bytes.NewBuffer(forceMsgBytes))

		if err == nil {
			defer forceResp.Body.Close()
			forceRespBytes, _ := io.ReadAll(forceResp.Body)

			var rawForceMsg map[string]interface{}
			forceAgentResponse := ""
			var forceToolCalls []map[string]interface{}

			if err := json.Unmarshal(forceRespBytes, &rawForceMsg); err == nil {
				if parts, ok := rawForceMsg["parts"].([]interface{}); ok {
					var text strings.Builder
					for _, p := range parts {
						if part, ok := p.(map[string]interface{}); ok {
							partType, _ := part["type"].(string)

							switch partType {
							case "tool-invocation":
								toolName, _ := part["toolName"].(string)
								toolArgs, _ := part["args"].(map[string]interface{})
								toolResult, _ := part["result"].(string)
								
								forceToolCalls = append(forceToolCalls, map[string]interface{}{
									"toolName": toolName,
									"args":     toolArgs,
								})

								logEntry("tool_call",
									fmt.Sprintf("🔧 Tool Call: %s", toolName),
									fmt.Sprintf("Args: %v\nResult: %s", toolArgs, toolResult),
									map[string]interface{}{
										"toolName":     toolName,
										"toolParams":   toolArgs,
										"responseSize": len(toolResult),
									})

							default:
								if t, ok := part["text"].(string); ok && t != "" {
									text.WriteString(t)
									text.WriteString("\n")
								}
							}
						}
					}
					forceAgentResponse = strings.TrimSpace(text.String())
				}
			}

			// Log force response
			logEntry("response",
				fmt.Sprintf("📥 Response from Model (%d chars)", len(forceAgentResponse)),
				forceAgentResponse,
				map[string]interface{}{
					"model":       agent.Model,
					"responseLen": len(forceAgentResponse),
					"toolCalls":   len(forceToolCalls),
				})

			// Fetch all session messages to get intermediate tool calls after force update
			forceSessionMsgsResp, err := client.Get(fmt.Sprintf("%s/session/%s/message", baseURL, sessionData.ID))
			if err == nil {
				defer forceSessionMsgsResp.Body.Close()
				forceSessionMsgsBytes, _ := io.ReadAll(forceSessionMsgsResp.Body)
				
				var forceSessionMsgs []map[string]interface{}
				if err := json.Unmarshal(forceSessionMsgsBytes, &forceSessionMsgs); err == nil {
					for _, msg := range forceSessionMsgs {
						info, _ := msg["info"].(map[string]interface{})
						role, _ := info["role"].(string)
						if role != "assistant" {
							continue
						}
						
						parts, _ := msg["parts"].([]interface{})
						for _, p := range parts {
							part, ok := p.(map[string]interface{})
							if !ok {
								continue
							}
							partType, _ := part["type"].(string)
							
							switch partType {
							case "tool":
								toolName, _ := part["tool"].(string)
								state, _ := part["state"].(map[string]interface{})
								toolStatus, _ := state["status"].(string)
								input, _ := state["input"].(map[string]interface{})
								output, _ := state["output"].(string)
								title, _ := state["title"].(string)
								
								preview := output
								if len(preview) > 200 {
									preview = preview[:200] + "..."
								}
								
								displayName := toolName
								if title != "" {
									displayName = fmt.Sprintf("%s (%s)", toolName, title)
								}
								
								logEntry("tool_call",
									fmt.Sprintf("🔧 Tool: %s [%s]", displayName, toolStatus),
									preview,
									map[string]interface{}{
										"toolName":     toolName,
										"toolParams":   input,
										"responseSize": len(output),
										"status":       toolStatus,
									})
							}
						}
					}
				}
			}

			if forceAgentResponse == "" {
				forceAgentResponse = string(forceRespBytes)
			}

			if forceAgentResponse != "" {
				comment, _ := e.q.CreateComment(ctx, db.Comment{
					TaskID:     task.ID,
					AuthorType: "agent",
					Content:    forceAgentResponse,
				})
				e.hub.BroadcastEvent("comment_created", comment)
			}
		} else {
			logEntry("error", fmt.Sprintf("❌ Failed to send force message: %v", err), "", nil)
		}
	}

	// Add final status entry
	if status == "failed" {
		logEntry("error", fmt.Sprintf("❌ Run failed. Check logs above for details."), "", nil)
	} else {
		logEntry("info", "✅ Run completed successfully.", "", nil)
	}

	// Update final status
	logJSON, _ := json.Marshal(logEntries)
	e.q.UpdateRunLog(ctx, run.ID, string(logJSON), status)

	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": status})
}
