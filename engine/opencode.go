package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/git"
	"agent-orchestrator/pkg/utils"
	"gorm.io/gorm"
)

type OpenCodeEngine struct {
	q           *db.Queries
	hub         *eventhub.Hub
	cancelFuncs sync.Map // runID -> context.CancelFunc
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

	q := db.New(database)

	// Start the OpenCode Server. The first call to runOpenCode will sync the
	// provider config and restart the server if needed, since the opencode
	// server doesn't hot-reload config.
	startOpenCodeServer(utils.IsE2E())

	return &OpenCodeEngine{
		q:   q,
		hub: hub,
	}
}

func (e *OpenCodeEngine) ProcessTask(ctx context.Context, taskID int32) error {
	task, err := e.q.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	if task.RunID != nil {
		isStale, err := e.q.IsRunStale(ctx, *task.RunID, 10*time.Minute)
		if err != nil {
			return fmt.Errorf("failed to check run staleness: %w", err)
		}
		if !isStale {
			return nil
		}
		fmt.Printf("Task %d has stale run %d, resolving...\n", taskID, *task.RunID)
		e.resolveStaleRun(ctx, *task.RunID)
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

func (e *OpenCodeEngine) resolveStaleRun(ctx context.Context, runID int32) {
	run, err := e.q.GetRun(ctx, runID)
	if err != nil {
		return
	}
	e.q.UpdateRunLog(ctx, runID, "Run marked as failed: server restarted while run was in progress", "failed")
	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": runID, "status": "failed"})
	e.q.UnlockTaskRun(ctx, run.TaskID)
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

	// Write run metadata to filesystem
	settings := loadEngineSettings()
	storage := filesystem.NewStorage(settings.BasePath)
	companyShortName, _ := storage.GetCompanyShortNameForTask(task.ID)
	if companyShortName != "" {
		if err := storage.WriteRun(run, companyShortName); err != nil {
			fmt.Printf("Warning: failed to write run metadata: %v\n", err)
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	e.cancelFuncs.Store(run.ID, cancel)
	defer func() {
		cancel()
		e.cancelFuncs.Delete(run.ID)
	}()

	if lockErr := e.q.LockTaskRun(ctx, task.ID, run.ID); lockErr != nil {
		fmt.Printf("Warning: failed to lock task %d for run %d: %v\n", task.ID, run.ID, lockErr)
	}

	defer func() {
		if clearErr := e.q.UnlockTaskRun(context.Background(), task.ID); clearErr != nil {
			fmt.Printf("Warning: failed to unlock task %d: %v\n", task.ID, clearErr)
		}
	}()

	e.hub.BroadcastEvent("run_started", run)

	// Sync the opencode provider config so the server (host in E2E mode,
	// Docker container in production mode) has the correct provider configuration.
	if syncErr := syncOpenCodeProviderConfig(e.q, run.ID); syncErr != nil {
		fmt.Printf("Warning: failed to sync opencode config: %v\n", syncErr)
	}

	// In E2E mode, restart the host opencode server so it picks up the new config.
	// The opencode server doesn't hot-reload config files, so a restart is the
	// only reliable way to apply config changes.
	if utils.IsE2E() {
		startOpenCodeServer(true)
	}

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

	settings = loadEngineSettings()
	fsMgr := filesystem.NewManager(settings.BasePath)

	company, compErr := e.q.GetCompany(ctx, task.CompanyID)
	if compErr != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to get company: %v", compErr))
		return
	}

	// Determine workspace path: always use task-specific directory
	workspacePath := fsMgr.GetTaskWorktreePath(company, task)

	// Create task workspace directory if it doesn't exist
	if err := os.MkdirAll(workspacePath, 0755); err != nil {
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to create task workspace: %v", err))
		return
	}

	if err := initTaskMemory(workspacePath, task, company); err != nil {
		logLine(fmt.Sprintf("Warning: failed to init memory.md: %v", err))
	}

	logLine(fmt.Sprintf("Using workspace: %s", workspacePath))

	// Git worktree setup for git projects
	var gitProject bool
	var gitMgr *git.GitManager
	var projectRepoDir string

	if task.ProjectID != nil {
		project, projErr := e.q.GetProject(ctx, *task.ProjectID)
		if projErr == nil && project.RepositoryUrl != "" {
			gitProject = true
			projectRepoDir = fsMgr.GetProjectRepoPath(company, project)
			sshDir := filepath.Join(settings.BasePath, ".ssh")
			gitMgr = git.NewGitManager(projectRepoDir, sshDir)

			// Pull latest in main repo
			if pullErr := gitMgr.Pull(ctx); pullErr != nil {
				logLine(fmt.Sprintf("Warning: git pull failed: %v", pullErr))
			}

			// Create worktree if it doesn't exist
			if _, statErr := os.Stat(workspacePath); os.IsNotExist(statErr) {
				branchName := fmt.Sprintf("task-%d", task.ID)
				if wtErr := gitMgr.CreateWorktree(ctx, projectRepoDir, workspacePath, branchName, "origin/main"); wtErr != nil {
					logLine(fmt.Sprintf("Failed to create worktree: %v", wtErr))
					gitProject = false
				} else {
					logLine(fmt.Sprintf("Created worktree for branch %s", branchName))
				}
			} else {
				logLine("Worktree already exists, reusing")
			}
		}
	}

	// In E2E mode we skip Docker entirely and talk to the host's OpenCode
	// server (port 36000) directly.
	e2eMode := utils.IsE2E()

	var baseURL string
	if e2eMode {
		baseURL = "http://127.0.0.1:36000"
		logLine("E2E mode: using host OpenCode server (no Docker sandbox)")
	} else {
		port := 36000 + int(run.ID%1000)
		sbx := &DockerSandbox{
			ImageName:     "paperclip-agent:latest",
			ContainerName: fmt.Sprintf("paperclip-agent-run-%d", run.ID),
			Port:          port,
		}
		logLine("Starting Docker Sandbox...")
		err = sbx.Run(ctx, workspacePath)
		if err != nil {
			e.failRun(ctx, run.ID, fmt.Sprintf("Failed to start docker sandbox: %v", err))
			return
		}
		defer sbx.Stop()

		baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	}

	// Create session
	sessionReqBody, _ := json.Marshal(map[string]interface{}{
		"title": fmt.Sprintf("Task %d: %s", task.ID, mode),
	})

	// Docker sandbox server is unsecured (no OPENCODE_SERVER_PASSWORD), so use plain client
	// E2E mode uses the host server which may require auth
	var sessionClient *http.Client
	if e2eMode {
		sessionClient = newOpenCodeHTTPClient(30 * time.Second)
	} else {
		sessionClient = &http.Client{Timeout: 30 * time.Second}
	}
	sessionResp, err := sessionClient.Post(baseURL+"/session", "application/json", bytes.NewBuffer(sessionReqBody))
	if err != nil || sessionResp.StatusCode != 200 {
		var bodySnippet string
		if sessionResp != nil {
			bodyBytes, _ := io.ReadAll(sessionResp.Body)
			bodySnippet = string(bodyBytes)
			sessionResp.Body.Close()
		}
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to create OpenCode session at %s: %v (status=%d body=%s)", baseURL, err, sessionRespOrStatus(sessionResp), bodySnippet))
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

	// Helper to keep the failure log readable even if sessionResp is nil
	// (Go's nil-interface quirk).

	e.q.UpdateRunLog(ctx, run.ID, fullLog.String(), "running") // Force save log
	e.q.UpdateRunSession(ctx, run.ID, sessionData.ID)
	logLine(fmt.Sprintf("Created session %s", sessionData.ID))

	// Give the server a moment to fully initialize the session
	time.Sleep(2 * time.Second)

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
			// Use background context to avoid any parent context cancellation issues
			provider, err := e.q.GetLLMProvider(context.Background(), *agent.ProviderID)
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

	msgReq, _ := http.NewRequestWithContext(runCtx, "POST", fmt.Sprintf("%s/session/%s/message", baseURL, sessionData.ID), bytes.NewBuffer(msgReqBytes))
	msgReq.Header.Set("Content-Type", "application/json")

	client := newOpenCodeHTTPClient(10 * time.Minute)
	msgResp, err := client.Do(msgReq)

	status := "completed"

	if err != nil {
		if runCtx.Err() == context.Canceled {
			logLine("Run canceled by user request")
			e.q.UpdateRunLog(ctx, run.ID, fullLog.String(), "canceled")
			e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": "canceled"})
			return
		}
		e.failRun(ctx, run.ID, fmt.Sprintf("Failed to send message: %v", err))
		return
	}
	defer msgResp.Body.Close()

	respBodyBytes, _ := io.ReadAll(msgResp.Body)
	e.q.TouchRunLastMessageTime(ctx, run.ID)
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
		forceReq, _ := http.NewRequestWithContext(runCtx, "POST", fmt.Sprintf("%s/session/%s/message", baseURL, sessionData.ID), bytes.NewBuffer(forceMsgBytes))
		forceReq.Header.Set("Content-Type", "application/json")
		forceResp, err := client.Do(forceReq)

		if err != nil {
			if runCtx.Err() == context.Canceled {
				logLine("Run canceled by user request")
				e.q.UpdateRunLog(ctx, run.ID, fullLog.String(), "canceled")
				e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": "canceled"})
				return
			}
			logLine(fmt.Sprintf("Failed to send force message: %v", err))
		} else {
			defer forceResp.Body.Close()
			forceRespBytes, _ := io.ReadAll(forceResp.Body)
			e.q.TouchRunLastMessageTime(ctx, run.ID)

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
		}
	}

	// Git commit after agent run
	if gitProject && gitMgr != nil && status == "completed" {
		logLine("Checking for changes to commit in worktree...")
		diff, diffErr := gitMgr.GetDiffInDir(ctx, workspacePath)
		if diffErr != nil {
			logLine(fmt.Sprintf("Warning: failed to get diff: %v", diffErr))
		} else if strings.TrimSpace(diff) != "" {
			commitMsg, msgErr := e.generateCommitMessage(ctx, agent, diff, task)
			if msgErr != nil {
				logLine(fmt.Sprintf("Warning: failed to generate commit message: %v, using fallback", msgErr))
				commitMsg = fmt.Sprintf("Agent run for task %d: %s", task.ID, mode)
			}
			if commitErr := gitMgr.CommitInWorktree(ctx, workspacePath, commitMsg); commitErr != nil {
				logLine(fmt.Sprintf("Warning: failed to commit: %v", commitErr))
			} else {
				logLine(fmt.Sprintf("Committed changes: %s", commitMsg))
			}
		} else {
			logLine("No changes to commit")
		}
	}

	e.q.UpdateRunLog(ctx, run.ID, fullLog.String(), status)

	// Update run metadata in filesystem
	settings = loadEngineSettings()
	storage = filesystem.NewStorage(settings.BasePath)
	companyShortName, _ = storage.GetCompanyShortNameForTask(task.ID)
	if companyShortName != "" {
		// Re-read the run to get updated fields
		updatedRun, err := e.q.GetRun(ctx, run.ID)
		if err == nil {
			storage.WriteRun(updatedRun, companyShortName)
		}
	}

	e.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": run.ID, "status": status})
}

func (e *OpenCodeEngine) generateCommitMessage(ctx context.Context, agent db.Agent, diff string, task db.Task) (string, error) {
	var providerBaseURL, apiKey, providerType, model string

	if agent.ProviderID != nil {
		provider, err := e.q.GetLLMProvider(ctx, *agent.ProviderID)
		if err != nil {
			return "", fmt.Errorf("failed to get provider: %w", err)
		}
		providerBaseURL = provider.BaseUrl
		apiKey = provider.ApiKey
		providerType = provider.ProviderType
		if providerType == "" {
			providerType = provider.Name
		}
		model = agent.Model
		if model == "" {
			model = provider.DefaultModel
		}
	} else {
		return "", fmt.Errorf("no provider configured for agent")
	}

	// Truncate diff if too long
	truncatedDiff := diff
	if len(diff) > 8000 {
		truncatedDiff = diff[:8000] + "\n... (truncated)"
	}

	prompt := fmt.Sprintf(`Summarize these code changes into a concise git commit message.
Subject line max 72 chars. Optional body separated by blank line.
Respond with ONLY the commit message, no quotes or explanation.

Task: %s
Changes:
%s`, task.Title, truncatedDiff)

	reqBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  200,
		"temperature": 0.3,
	}

	reqBytes, _ := json.Marshal(reqBody)

	chatURL := fmt.Sprintf("%s/v1/chat/completions", providerBaseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", chatURL, bytes.NewBuffer(reqBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	msg := strings.TrimSpace(result.Choices[0].Message.Content)
	// Remove surrounding quotes if present
	msg = strings.Trim(msg, "\"'`")
	return msg, nil
}

func loadEngineSettings() struct{ BasePath string } {
	return struct{ BasePath string }{BasePath: db.PaperclipHome()}
}

// sessionRespOrStatus returns the StatusCode of a possibly-nil *http.Response
// without panicking. Used to format session-creation failure messages.
func sessionRespOrStatus(r *http.Response) int {
	if r == nil {
		return 0
	}
	return r.StatusCode
}

// newOpenCodeHTTPClient returns an *http.Client configured to authenticate
// against the OpenCode server. When OPENCODE_SERVER_USERNAME and
// OPENCODE_SERVER_PASSWORD are set in the environment, the client sends an
// HTTP Basic auth header on every request.
func newOpenCodeHTTPClient(timeout time.Duration) *http.Client {
	username := os.Getenv("OPENCODE_SERVER_USERNAME")
	password := os.Getenv("OPENCODE_SERVER_PASSWORD")
	// If only password is set, use default username
	if username == "" && password != "" {
		username = "opencode"
	}
	fmt.Printf("newOpenCodeHTTPClient: username=%q, password=%q\n", username, password)
	if username == "" && password == "" {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &basicAuthTransport{
			username: username,
			password: password,
			base:     http.DefaultTransport,
		},
	}
}

type basicAuthTransport struct {
	username string
	password string
	base     http.RoundTripper
}

func (t *basicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Basic "+basicAuth(t.username, t.password))
	return t.base.RoundTrip(cloned)
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func (e *OpenCodeEngine) StopRun(ctx context.Context, runID int32) {
	if val, loaded := e.cancelFuncs.LoadAndDelete(runID); loaded {
		if cancel, ok := val.(context.CancelFunc); ok {
			cancel()
		}
	}
}
