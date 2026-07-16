package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/git"
	"github.com/go-chi/chi/v5"
)

func (api *API) ListTasks(w http.ResponseWriter, r *http.Request) {
	compIDStr := r.URL.Query().Get("company_id")
	if compIDStr == "" {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	compID, _ := strconv.Atoi(compIDStr)
	if _, err := api.authorizeCompany(r, int32(compID)); err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}

	query := api.db.Where("company_id = ?", compID)

	projIDsStr := r.URL.Query().Get("project_ids")
	if projIDsStr != "" {
		ids := strings.Split(projIDsStr, ",")
		query = query.Where("project_id IN ?", ids)
	}

	sprintIDsStr := r.URL.Query().Get("sprint_ids")
	if sprintIDsStr != "" {
		ids := strings.Split(sprintIDsStr, ",")
		query = query.Where("sprint_id IN ?", ids)
	}

	archivedStr := r.URL.Query().Get("archived")
	if archivedStr == "true" {
		query = query.Where("is_archived = ?", true)
	} else {
		query = query.Where("is_archived = ?", false)
	}

	priority := r.URL.Query().Get("priority")
	if priority != "" {
		query = query.Where("priority = ?", priority)
	}

	agentIDStr := r.URL.Query().Get("agent_id")
	if agentIDStr != "" {
		agentID, _ := strconv.Atoi(agentIDStr)
		query = query.Where("agent_id = ?", agentID)
	}

	// The grid shows main tasks only. Subtasks are internal delegation
	// artifacts; fetch them explicitly via parent_id or include_subtasks=true.
	parentIDStr := r.URL.Query().Get("parent_id")
	switch {
	case parentIDStr != "":
		parentID, _ := strconv.Atoi(parentIDStr)
		query = query.Where("parent_id = ?", parentID)
	case r.URL.Query().Get("include_subtasks") != "true":
		query = query.Where("parent_id IS NULL")
	}

	var tasks []db.Task
	if err := query.Order("id").Find(&tasks).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, tasks)
}

func (api *API) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID       int32   `json:"company_id"`
		ProjectID       *int32  `json:"project_id"`
		AgentID         *int32  `json:"agent_id"`
		SprintID        int32   `json:"sprint_id"`
		ParentID        *int32  `json:"parent_id"`
		Title           string  `json:"title"`
		TaskType        string  `json:"task_type"`
		Description     string  `json:"description"`
		Priority        string  `json:"priority"`
		DueDate         *string `json:"due_date"`
		AgentConfigName string  `json:"agent_config_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	var dueDate *time.Time
	if req.DueDate != nil {
		t, _ := time.Parse(time.RFC3339, *req.DueDate)
		dueDate = &t
	}

	priority := req.Priority
	if priority == "" {
		priority = "Normal"
	}

	taskType := req.TaskType
	if taskType == "" {
		taskType = db.TaskTypePlanAndImplement
	}

	if req.CompanyID == 0 {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	if _, err := api.authorizeCompany(r, req.CompanyID); err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}

	p := db.Task{
		CompanyID:       req.CompanyID,
		ProjectID:       req.ProjectID,
		Title:           req.Title,
		TaskType:        taskType,
		Status:          "backlog",
		AgentID:         req.AgentID,
		SprintID:        req.SprintID,
		ParentID:        req.ParentID,
		Description:     req.Description,
		Priority:        priority,
		DueDate:         dueDate,
		AgentConfigName: req.AgentConfigName,
	}

	task, err := api.q.CreateTask(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.hub.BroadcastEventForCompany(task.CompanyID, "task_created", task)

	var comp db.Company
	api.db.First(&comp, req.CompanyID)

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)

	if req.ProjectID != nil {
		var proj db.Project
		api.db.First(&proj, *req.ProjectID)
		fsManager.CreateTaskWorkspace(comp, proj, task)

		// Write task metadata to filesystem
		storage := filesystem.NewStorage(settings.BasePath)
		if err := storage.WriteTask(task, comp.ShortName); err != nil {
			log.Printf("Warning: failed to write task metadata: %v", err)
		}
	}

	fsManager.SaveTask(comp, task)

	api.logActivity(comp.ID, "task_created", int32(task.ID), "task", "")

	api.respondJSON(w, http.StatusCreated, task)
}

func (api *API) GetTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	task, err := api.authorizeTask(r, int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "task not found")
		return
	}
	api.respondJSON(w, http.StatusOK, task)
}

func (api *API) UpdateTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		ProjectID       *int32  `json:"project_id"`
		AgentID         *int32  `json:"agent_id"`
		SprintID        *int32  `json:"sprint_id"`
		ParentID        *int32  `json:"parent_id"`
		Title           string  `json:"title"`
		TaskType        string  `json:"task_type"`
		Description     string  `json:"description"`
		Priority        string  `json:"priority"`
		DueDate         *string `json:"due_date"`
		Status          string  `json:"status"`
		IsArchived      *bool   `json:"is_archived"`
		AgentConfigName string  `json:"agent_config_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	task, err := api.authorizeTask(r, int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "Task not found")
		return
	}

	statusChanged := false
	prevStatus := task.Status
	if req.Status != "" && req.Status != task.Status {
		task.Status = req.Status
		statusChanged = true
	}
	if req.TaskType != "" {
		task.TaskType = req.TaskType
	}

	if req.Title != "" {
		task.Title = req.Title
	}
	if req.Description != "" {
		task.Description = req.Description
	}
	if req.Priority != "" {
		task.Priority = req.Priority
	}

	if req.ProjectID != nil {
		task.ProjectID = req.ProjectID
	}
	if req.AgentID != nil {
		task.AgentID = req.AgentID
	}
	if req.SprintID != nil {
		task.SprintID = *req.SprintID
	}
	if req.ParentID != nil {
		task.ParentID = req.ParentID
	}
	if req.IsArchived != nil {
		task.IsArchived = *req.IsArchived
	}
	if req.AgentConfigName != "" {
		task.AgentConfigName = req.AgentConfigName
	}

	if req.DueDate != nil {
		t, _ := time.Parse(time.RFC3339, *req.DueDate)
		task.DueDate = &t
	}

	task, err = api.q.UpdateTask(r.Context(), task)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", task)

	if statusChanged {
		content, _ := json.Marshal(map[string]string{"from": prevStatus, "to": task.Status})
		if sc, err := api.q.CreateComment(r.Context(), db.Comment{
			TaskID:      task.ID,
			AuthorType:  "human",
			CommentType: "status_change",
			Content:     string(content),
		}); err == nil {
			api.hub.BroadcastEventForCompany(task.CompanyID, "comment_created", sc)
		}
	}

	var taskComp db.Company
	if api.db.First(&taskComp, task.CompanyID).Error == nil {
		taskSettings := LoadSettings()
		filesystem.NewManager(taskSettings.BasePath).SaveTask(taskComp, task)
	}

	api.logActivity(task.CompanyID, "task_updated", int32(task.ID), "task", "")

	// Git lifecycle: handle worktree merge/reopen on status change
	// Executed async to avoid blocking HTTP response. Errors are logged and
	// broadcast via event hub. Tests use polling (waitForTaskStatus) to handle
	// async execution.
	if statusChanged && task.ProjectID != nil {
		go api.handleGitLifecycle(task, req.Status)
	}

	// ProcessTask runs the LLM agent. Executed async because LLM calls can
	// take minutes. The agent updates task status via tool calls, and tests
	// poll for status changes.
	if statusChanged {
		go api.engine.ProcessTask(context.Background(), int32(id))
	}

	api.respondJSON(w, http.StatusOK, task)
}

func (api *API) ListTaskRuns(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := api.authorizeTask(r, int32(id)); err != nil {
		api.respondError(w, http.StatusNotFound, "task not found")
		return
	}
	var runs []db.Run
	if err := api.db.Where("task_id = ?", id).Order("started_at desc").Find(&runs).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]RunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunResponse(run))
	}
	api.respondJSON(w, http.StatusOK, out)
}

func (api *API) handleGitLifecycle(task db.Task, newStatus string) {
	ctx := context.Background()

	project, err := api.q.GetProject(ctx, *task.ProjectID)
	if err != nil || project.RepositoryUrl == "" {
		return
	}

	company, err := api.q.GetCompany(ctx, task.CompanyID)
	if err != nil {
		return
	}

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	repoDir := fsManager.GetProjectRepoPath(company, project)
	worktreeDir := fsManager.GetTaskWorktreePath(company, task)
	sshDir := filepath.Join(settings.BasePath, ".ssh")
	gitMgr := git.NewGitManager(repoDir, sshDir)

	branchName := fmt.Sprintf("task-%d", task.ID)

	if newStatus == "done" {
		// Merge worktree branch into main
		if _, statErr := os.Stat(worktreeDir); statErr == nil {
			if mergeErr := gitMgr.MergeBranch(ctx, repoDir, branchName); mergeErr != nil {
				fmt.Printf("Warning: failed to merge branch %s: %v\n", branchName, mergeErr)
				return
			}
			if removeErr := gitMgr.RemoveWorktree(ctx, worktreeDir); removeErr != nil {
				fmt.Printf("Warning: failed to remove worktree: %v\n", removeErr)
			}
			// Clean up the task workspace directory
			os.RemoveAll(worktreeDir)
			fmt.Printf("Merged and cleaned up worktree for task %d\n", task.ID)
		}
	} else if task.Status == "done" && newStatus != "done" {
		// Task reopened: remove old worktree and recreate
		if _, statErr := os.Stat(worktreeDir); statErr == nil {
			gitMgr.RemoveWorktree(ctx, worktreeDir)
			os.RemoveAll(worktreeDir)
		}
		// Pull latest and recreate worktree
		gitMgr.Pull(ctx)
		if wtErr := gitMgr.CreateWorktree(ctx, repoDir, worktreeDir, branchName, "origin/main"); wtErr != nil {
			fmt.Printf("Warning: failed to recreate worktree for task %d: %v\n", task.ID, wtErr)
		} else {
			fmt.Printf("Recreated worktree for reopened task %d\n", task.ID)
		}
	}
}
