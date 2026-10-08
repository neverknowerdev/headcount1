package endpoints

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/engine/classifier"
	"agent-orchestrator/pkg/filesystem"
	"agent-orchestrator/pkg/git"
	"agent-orchestrator/pkg/githubapp"

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
	api.attachTaskRelationSummaries(r.Context(), tasks)

	api.respondJSON(w, http.StatusOK, tasks)
}

// authorizeTaskRefs verifies every object a task references — project, agent,
// sprint, parent — belongs to the SAME company as the task. project_id/agent_id
// are sequential int32; without this a user could point a task in their own
// company at another tenant's project or agent and drive a worktree/agent from
// a foreign record. companyID is the task's already-authorized company.
func (api *API) authorizeTaskRefs(r *http.Request, companyID int32, projectID, agentID, sprintID, parentID *int32) error {
	if projectID != nil {
		proj, err := api.authorizeProject(r, *projectID)
		if err != nil || proj.CompanyID != companyID {
			return errNotOwned
		}
	}
	if agentID != nil {
		agent, err := api.authorizeAgent(r, *agentID)
		if err != nil || agent.CompanyID != companyID || !agent.Enabled {
			return errNotOwned
		}
	}
	if sprintID != nil && *sprintID != 0 {
		var s db.Sprint
		if err := api.db.WithContext(r.Context()).First(&s, *sprintID).Error; err != nil || s.CompanyID != companyID {
			return errNotOwned
		}
	}
	if parentID != nil {
		var p db.Task
		if err := api.db.WithContext(r.Context()).First(&p, *parentID).Error; err != nil || p.CompanyID != companyID {
			return errNotOwned
		}
	}
	return nil
}

func (api *API) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID     int32   `json:"company_id"`
		ProjectID     *int32  `json:"project_id"`
		AgentID       *int32  `json:"agent_id"`
		SprintID      int32   `json:"sprint_id"`
		ParentID      *int32  `json:"parent_id"`
		Title         string  `json:"title"`
		Description   string  `json:"description"`
		Priority      string  `json:"priority"`
		GitBaseBranch string  `json:"git_base_branch"`
		DueDate       *string `json:"due_date"`
		TaskType      string  `json:"task_type"`
		ProviderID    *int32  `json:"provider_id"`
		ModelGroupID  *int32  `json:"model_group_id"`
		Model         string  `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	if req.TaskType == "" {
		req.TaskType = db.TaskTypeGeneral
	}
	if !isTaskType(req.TaskType) {
		api.respondError(w, http.StatusBadRequest, "task_type must be research, coding, review or general")
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

	gitBaseBranch := strings.TrimSpace(req.GitBaseBranch)
	if gitBaseBranch == "" {
		gitBaseBranch = db.DefaultTaskGitBaseBranch
	}
	if err := git.ValidateBranchName(r.Context(), gitBaseBranch); err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.CompanyID == 0 {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	if _, err := api.authorizeCompany(r, req.CompanyID); err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}
	sprintPtr := &req.SprintID
	if err := api.authorizeTaskRefs(r, req.CompanyID, req.ProjectID, req.AgentID, sprintPtr, req.ParentID); err != nil {
		api.respondError(w, http.StatusNotFound, "a referenced project, agent, sprint, or parent task was not found")
		return
	}
	model, err := api.taskModel(r, req.ProviderID, req.ModelGroupID, req.Model)
	if err != nil {
		api.respondError(w, http.StatusNotFound, "provider or model group not found")
		return
	}

	// A task a person creates is driven by a smart model through the workflow
	// of its type; the workflow creates the direct tasks that do the work.
	p := db.Task{
		CompanyID:     req.CompanyID,
		ProjectID:     req.ProjectID,
		Title:         req.Title,
		Status:        db.TaskStatusBacklog,
		AgentID:       req.AgentID,
		SprintID:      req.SprintID,
		ParentID:      req.ParentID,
		Description:   req.Description,
		Priority:      priority,
		GitBaseBranch: gitBaseBranch,
		DueDate:       dueDate,
		TaskType:      req.TaskType,
		Mode:          db.TaskModeManaged,
		ProviderID:    model.providerID,
		ModelGroupID:  model.modelGroupID,
		Model:         model.model,
	}

	task, err := api.q.CreateTask(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.hub.BroadcastEventForCompany(task.CompanyID, "task_created", task)

	var comp db.Company
	api.db.First(&comp, req.CompanyID)

	if req.ProjectID != nil {
		settings := LoadSettings()
		var proj db.Project
		api.db.First(&proj, *req.ProjectID)
		filesystem.NewManager(settings.BasePath).CreateTaskWorkspace(comp, proj, task)
	}

	api.logActivity(comp.ID, "task_created", int32(task.ID), "task", "")

	tasks := []db.Task{task}
	api.attachTaskRelationSummaries(r.Context(), tasks)
	task = tasks[0]
	api.respondJSON(w, http.StatusCreated, task)
}

func isTaskType(taskType string) bool {
	switch taskType {
	case db.TaskTypeResearch, db.TaskTypeCoding, db.TaskTypeReview, db.TaskTypeGeneral:
		return true
	}
	return false
}

// isHumanTaskStatus reports whether a person may put a task in a status.
// Every other status says what the workflow is doing and is its to set.
func isHumanTaskStatus(status string) bool {
	switch status {
	case db.TaskStatusBacklog, db.TaskStatusTodo, db.TaskStatusInReview, db.TaskStatusDone:
		return true
	}
	return false
}

// isTaskRunning reports whether the workflow is working on a task or about to.
func isTaskRunning(status string) bool {
	switch status {
	case db.TaskStatusTodo, db.TaskStatusDependsOnTask, db.TaskStatusInProgress:
		return true
	}
	return false
}

// taskModelOverride is a task's own model: a provider with a model, a model
// group, or nothing, which means the default of its tier.
type taskModelOverride struct {
	providerID   *int32
	modelGroupID *int32
	model        string
}

// taskModel validates a requested model override. A model group takes
// precedence over a provider; a provider without a model is no override.
func (api *API) taskModel(r *http.Request, providerID, modelGroupID *int32, model string) (taskModelOverride, error) {
	if err := api.authorizeModelBinding(r, providerID, modelGroupID); err != nil {
		return taskModelOverride{}, err
	}
	if providerID != nil && modelGroupID == nil {
		// A classifier answers yes/no questions; it cannot run a task.
		if provider, err := api.q.GetLLMProvider(r.Context(), *providerID); err != nil || provider.ProviderType == classifier.ProviderType {
			return taskModelOverride{}, errNotOwned
		}
	}
	model = strings.TrimSpace(model)
	switch {
	case modelGroupID != nil:
		return taskModelOverride{modelGroupID: modelGroupID}, nil
	case providerID != nil && model != "":
		return taskModelOverride{providerID: providerID, model: model}, nil
	}
	return taskModelOverride{}, nil
}

func (api *API) GetTask(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	tasks := []db.Task{task}
	api.attachTaskRelationSummaries(r.Context(), tasks)
	task = tasks[0]
	api.respondJSON(w, http.StatusOK, task)
}

func (api *API) attachTaskRelationSummaries(ctx context.Context, tasks []db.Task) {
	ids := make([]int32, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	summaries, err := api.q.ListTaskRelationSummaries(ctx, ids)
	if err != nil {
		return
	}
	for i := range tasks {
		if summary, ok := summaries[tasks[i].ID]; ok {
			tasks[i].RelationSummary = &summary
		}
	}
}

func (api *API) reconcileDependents(ctx context.Context, prerequisiteTaskID int32) {
	dependents, err := api.q.ListDependentTasks(ctx, prerequisiteTaskID)
	if err != nil {
		return
	}
	for _, dependent := range dependents {
		id := dependent.ID
		go func() {
			if err := api.engine.ProcessTask(context.Background(), id); err != nil {
				fmt.Printf("Warning: failed to reconcile dependent task %d: %v\n", id, err)
			}
		}()
	}
}

// UpdateTask edits a task. Its status is the workflow's while it runs, so a
// person can only do three things with it: queue the task (to-do), which also
// sends a finished or parked task back to work; stop it (see StopTask); and
// place a task that is at rest in the backlog, in review, or done.
func (api *API) UpdateTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID     *int32  `json:"project_id"`
		AgentID       *int32  `json:"agent_id"`
		SprintID      *int32  `json:"sprint_id"`
		ParentID      *int32  `json:"parent_id"`
		Title         string  `json:"title"`
		Description   string  `json:"description"`
		Priority      string  `json:"priority"`
		GitBaseBranch string  `json:"git_base_branch"`
		DueDate       *string `json:"due_date"`
		Status        string  `json:"status"`
		IsArchived    *bool   `json:"is_archived"`
		TaskType      string  `json:"task_type"`
		// The three model fields are replaced together when any is present;
		// sending an empty model alone clears the override.
		ProviderID   *int32  `json:"provider_id"`
		ModelGroupID *int32  `json:"model_group_id"`
		Model        *string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	task := api.taskFromCtx(r) // loaded + authorized by LoadTask

	if req.Status == task.Status {
		req.Status = ""
	}
	if req.Status != "" && !isHumanTaskStatus(req.Status) {
		api.respondError(w, http.StatusConflict, "that status is set by the workflow; a task can be moved to backlog, to-do, in-review or done")
		return
	}
	if req.TaskType != "" && !isTaskType(req.TaskType) {
		api.respondError(w, http.StatusBadRequest, "task_type must be research, coding, review or general")
		return
	}

	// Referenced objects must belong to the task's company (same tenancy check
	// as CreateTask) — a foreign project_id/agent_id must never be bound here.
	if err := api.authorizeTaskRefs(r, task.CompanyID, req.ProjectID, req.AgentID, req.SprintID, req.ParentID); err != nil {
		api.respondError(w, http.StatusNotFound, "a referenced project, agent, sprint, or parent task was not found")
		return
	}

	// Only the columns this request names are written. The task in context was
	// read by middleware before this handler ran; saving that copy whole would
	// overwrite anything the engine changed since.
	updates := map[string]interface{}{}
	if req.Title != "" {
		updates["title"] = req.Title
	}
	if req.Description != "" {
		updates["description"] = req.Description
	}
	if req.Priority != "" {
		updates["priority"] = req.Priority
	}
	if req.GitBaseBranch != "" && req.GitBaseBranch != task.GitBaseBranch {
		if task.Status != db.TaskStatusBacklog || req.Status != "" {
			api.respondError(w, http.StatusConflict, "base branch cannot be changed after work has started")
			return
		}
		branch := strings.TrimSpace(req.GitBaseBranch)
		if err := git.ValidateBranchName(r.Context(), branch); err != nil {
			api.respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		updates["git_base_branch"] = branch
	}
	if req.TaskType != "" && req.TaskType != task.TaskType {
		// The type selects the phases a task goes through; changing it under
		// a workflow in flight would leave the task in a phase it does not have.
		if isTaskRunning(task.Status) || task.Status == db.TaskStatusBlocked {
			api.respondError(w, http.StatusConflict, "the task type cannot be changed while the task is running; stop it first")
			return
		}
		updates["task_type"] = req.TaskType
	}
	if req.ProviderID != nil || req.ModelGroupID != nil || req.Model != nil {
		name := ""
		if req.Model != nil {
			name = *req.Model
		}
		model, err := api.taskModel(r, req.ProviderID, req.ModelGroupID, name)
		if err != nil {
			api.respondError(w, http.StatusNotFound, "provider or model group not found")
			return
		}
		updates["provider_id"] = model.providerID
		updates["model_group_id"] = model.modelGroupID
		updates["model"] = model.model
	}
	if req.ProjectID != nil {
		updates["project_id"] = *req.ProjectID
	}
	if req.AgentID != nil {
		updates["agent_id"] = *req.AgentID
	}
	if req.SprintID != nil {
		updates["sprint_id"] = *req.SprintID
	}
	if req.ParentID != nil {
		updates["parent_id"] = *req.ParentID
	}
	if req.IsArchived != nil {
		if *req.IsArchived && isTaskRunning(task.Status) && req.Status == "" {
			// An archived task is never looked at again, so one that is running
			// would be left that way.
			api.respondError(w, http.StatusConflict, "the task is running; stop it before archiving it")
			return
		}
		updates["is_archived"] = *req.IsArchived
	}
	if req.DueDate != nil {
		t, _ := time.Parse(time.RFC3339, *req.DueDate)
		updates["due_date"] = t
	}

	// The status goes through the engine, which owns it, before anything else
	// is written: a move it refuses must not leave half an edit behind.
	prevStatus := task.Status
	if req.Status != "" {
		var err error
		if req.Status == db.TaskStatusTodo {
			err = api.engine.RerunTask(r.Context(), task.ID)
		} else {
			err = api.engine.SetTaskStatus(r.Context(), task.ID, req.Status)
		}
		if err != nil {
			api.respondEngineError(w, err)
			return
		}
	}

	updatedTask, err := api.q.UpdateTaskFields(r.Context(), task.ID, updates)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	task = updatedTask
	api.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", task)

	// The engine has already put the move on the task's record.
	if task.Status != prevStatus {
		if task.Status == db.TaskStatusDone {
			api.reconcileDependents(r.Context(), task.ID)
		}
		// Git lifecycle: merge or keep the worktree on a status change. It runs
		// in the background so the response is not held up; errors are logged.
		if task.ProjectID != nil {
			go api.handleGitLifecycle(task, task.Status)
		}
	}

	api.logActivity(task.CompanyID, "task_updated", int32(task.ID), "task", "")

	api.respondJSON(w, http.StatusOK, task)
}

// respondEngineError maps a refusal from the engine to its HTTP answer.
func (api *API) respondEngineError(w http.ResponseWriter, err error) {
	var blocked *engine.TaskDependencyBlockedError
	switch {
	case errors.As(err, &blocked):
		api.respondJSON(w, http.StatusConflict, map[string]interface{}{"error": err.Error(), "task_id": blocked.TaskID, "blocked_by": blocked.Blockers})
	case errors.Is(err, engine.ErrTaskRunning):
		api.respondError(w, http.StatusConflict, err.Error())
	default:
		api.respondError(w, http.StatusInternalServerError, err.Error())
	}
}

// RerunTask sends a finished, parked or stuck task back to work. Asking for a
// subtask reruns the task at the top of its tree.
func (api *API) RerunTask(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	if err := api.engine.RerunTask(r.Context(), task.ID); err != nil {
		api.respondEngineError(w, err)
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]interface{}{"status": "queued", "task_id": task.RootTaskID})
}

// StopTask halts a task and everything under it. A top-level task parks until
// it is run again; a subtask is canceled and its parent deals with that.
func (api *API) StopTask(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	if err := api.engine.StopTask(r.Context(), task.ID); err != nil {
		api.respondEngineError(w, err)
		return
	}
	stopped, err := api.q.GetTask(r.Context(), task.ID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, stopped)
}

// ListTaskTree returns a task and every task beneath it, parents before
// children, each with its dependencies.
func (api *API) ListTaskTree(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	tasks, err := api.q.ListTaskSubtree(r.Context(), task.ID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.attachTaskRelationSummaries(r.Context(), tasks)
	api.respondJSON(w, http.StatusOK, tasks)
}

// ListTaskSteps returns a task's journal in order: phase changes, smart calls,
// subtasks, questions and executor sessions. The prompt and answer of a smart
// call are left out here; GetTaskStep returns one step in full.
func (api *API) ListTaskSteps(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	steps, err := api.q.ListTaskStepHeads(r.Context(), task.ID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, steps)
}

func (api *API) GetTaskStep(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	stepID, err := strconv.ParseInt(chi.URLParam(r, "stepID"), 10, 64)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid step id")
		return
	}
	step, err := api.q.GetTaskStep(r.Context(), stepID)
	if err != nil || step.TaskID != task.ID {
		api.respondError(w, http.StatusNotFound, "step not found")
		return
	}
	api.respondJSON(w, http.StatusOK, step)
}

// ListTaskDecisions returns what was decided on a task, oldest first. With
// subtree=true it covers every task beneath it as well; the task tree gives
// the decisions their hierarchy.
func (api *API) ListTaskDecisions(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	if r.URL.Query().Get("subtree") != "true" {
		decisions, err := api.q.ListDecisionsByTask(r.Context(), task.ID)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		api.respondJSON(w, http.StatusOK, decisions)
		return
	}
	subtree, err := api.q.ListTaskSubtree(r.Context(), task.ID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	within := make(map[int32]bool, len(subtree))
	for _, member := range subtree {
		within[member.ID] = true
	}
	all, err := api.q.ListDecisionsByRoot(r.Context(), task.RootTaskID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	decisions := make([]db.Decision, 0, len(all))
	for _, decision := range all {
		if within[decision.TaskID] {
			decisions = append(decisions, decision)
		}
	}
	api.respondJSON(w, http.StatusOK, decisions)
}

// DownloadTaskLogs streams a task's logs as a zip: its journal, its executor
// sessions and its decisions, and for a top-level task those of every task
// beneath it.
func (api *API) DownloadTaskLogs(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	company, err := api.q.GetCompany(r.Context(), task.CompanyID)
	if err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return
	}
	paths := filesystem.NewPaths(LoadSettings().BasePath)
	logDir := paths.TaskLogsDir(company.ShortName, task.RootTaskID)
	if task.RootTaskID != task.ID {
		logDir = paths.TaskJournalDir(company.ShortName, task.RootTaskID, task.ID)
	}
	var files []string
	err = filepath.WalkDir(logDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		api.respondError(w, http.StatusInternalServerError, "failed to read the task's logs")
		return
	}
	if len(files) == 0 {
		api.respondError(w, http.StatusNotFound, "the task has no logs")
		return
	}

	name := fmt.Sprintf("task-%d-logs.zip", task.ID)
	if task.RefKey != "" {
		name = task.RefKey + "-logs.zip"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, path := range files {
		rel, err := filepath.Rel(logDir, path)
		if err != nil {
			return
		}
		file, err := os.Open(path)
		if err != nil {
			return
		}
		writer, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			file.Close()
			return
		}
		_, _ = io.Copy(writer, file)
		file.Close()
	}
}

func (api *API) ListTaskRuns(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	var runs []db.Run
	if err := api.db.Preload("Agent").Where("task_id = ?", task.ID).Order("started_at desc").Find(&runs).Error; err != nil {
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
	rootTask, rootErr := api.q.GetRootTask(ctx, task.ID)
	if rootErr != nil {
		rootTask = task
	}

	if rootTask.ProjectID == nil {
		return
	}
	project, err := api.q.GetProject(ctx, *rootTask.ProjectID)
	if err != nil || project.RepositoryUrl == "" {
		return
	}

	company, err := api.q.GetCompany(ctx, rootTask.CompanyID)
	if err != nil {
		return
	}

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	repoDir := fsManager.GetProjectRepoPath(company, project)
	worktreeDir := fsManager.GetTaskWorktreePath(company, rootTask)
	keyPath, keyCleanup := filesystem.ResolveSSHKeyPathForCompany(ctx, api.q, settings.BasePath, company)
	defer keyCleanup()
	gitMgr := git.NewGitManager(repoDir, keyPath)

	branchName := strings.TrimSpace(rootTask.GitHubBranch)
	if branchName == "" {
		branchName = db.TaskGitBranch(rootTask.RefKey, rootTask.ID)
		rootTask.GitHubBranch = branchName
		if _, updateErr := api.q.UpdateTaskFields(ctx, rootTask.ID, map[string]interface{}{"git_hub_branch": branchName}); updateErr != nil {
			fmt.Printf("Warning: failed to persist task branch %s: %v\n", branchName, updateErr)
			return
		}
	}
	if project.GitHubInstallationID != 0 {
		if token, tokenErr := githubapp.TokenForProject(ctx, project); tokenErr == nil && token != "" {
			gitMgr.WithHTTPToken(token)
		} else if tokenErr != nil {
			fmt.Printf("Warning: failed to create GitHub App token for project %d: %v\n", project.ID, tokenErr)
			return
		}
		// GitHub-backed projects publish a draft PR. Never merge or force-push
		// the default branch as part of task status changes.
		// The task-level branch is already canonical and shared by all runs.
		if newStatus == "done" {
			return
		}
	}

	if newStatus == "done" {
		// Merge worktree branch into main
		if _, statErr := os.Stat(worktreeDir); statErr == nil {
			if mergeErr := gitMgr.MergeBranch(ctx, repoDir, branchName); mergeErr != nil {
				fmt.Printf("Warning: failed to merge branch %s: %v\n", branchName, mergeErr)
				return
			}
			// Keep the worktree and durable session workspaces until the in-binary
			// retention job removes them ten days after the task became Done.
			fmt.Printf("Merged worktree for task %d; deferred cleanup is scheduled\n", task.ID)
		}
	} else if task.Status == "done" && newStatus != "done" {
		// Reopening within the retention window reuses the preserved worktree;
		// deleting and recreating it would discard the exact session state a
		// fork or recovery may need.
		fmt.Printf("Reopened task %d; preserving its worktree\n", task.ID)
	}
}
