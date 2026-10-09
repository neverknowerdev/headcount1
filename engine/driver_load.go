package engine

import (
	"context"
	"encoding/json"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/workflow"
)

// loadedTask is a task as the driver read it for one decision, with the
// snapshot the workflow evaluates and the rows that snapshot was built from.
type loadedTask struct {
	task     db.Task
	company  db.Company
	children []db.WorkflowChild
	steps    []db.TaskStep
	agents   []db.Agent
	target   modelTarget
	answer   *db.Comment
	snapshot workflow.Snapshot
}

// load reads everything about a task that decides its next step.
func (d *workflowDriver) load(ctx context.Context, taskID int32) (*loadedTask, error) {
	task, err := d.q.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	l := &loadedTask{task: task, company: task.Company}
	s := workflow.Snapshot{
		Task: workflow.Task{
			ID: task.ID, IsRoot: task.ParentID == nil, Depth: task.Depth,
			Type: task.TaskType, Mode: task.Mode, Status: task.Status, Phase: task.Phase,
			WaitingOn: task.WaitingOn, WaitUntil: task.WaitUntil,
			SmartStepsUsed: task.SmartStepsUsed, AdjustCyclesUsed: task.AdjustCyclesUsed,
			AttemptsUsed: task.AttemptsUsed, ReviewRound: task.ReviewRound,
		},
		Now: d.now(),
	}

	switch task.Status {
	case models.TaskStatusTodo, models.TaskStatusDependsOnTask:
		prerequisites, err := d.q.ListPrerequisites(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		for _, prerequisite := range prerequisites {
			s.Prerequisites = append(s.Prerequisites, workflow.Prerequisite{
				Status: prerequisite.Status, Type: prerequisite.TaskType,
				Verdict: prerequisite.ResultVerdict, ReviewRound: prerequisite.ReviewRound,
			})
		}
		if task.ParentID != nil && len(prerequisites) > 0 {
			// Whether a review that asked for changes gets a fix round is the
			// parent's workflow's business.
			parent, err := d.q.GetTask(ctx, *task.ParentID)
			if err != nil {
				return nil, err
			}
			s.Task.ParentType = parent.TaskType
		}
	case models.TaskStatusInProgress, models.TaskStatusBlocked:
	default:
		// Not started or finished: nothing else matters.
		l.snapshot = s
		return l, nil
	}

	if l.agents, err = d.q.ListAgentsByCompany(ctx, task.CompanyID); err != nil {
		return nil, err
	}
	agentNames := map[int32]string{}
	for _, agent := range l.agents {
		agentNames[agent.ID] = agent.Name
		if agent.Enabled {
			s.Roles = append(s.Roles, agent.Name)
		}
	}

	if task.Mode == models.TaskModeManaged {
		if l.children, err = d.q.ListWorkflowChildren(ctx, task.ID); err != nil {
			return nil, err
		}
		for _, child := range l.children {
			role := ""
			if child.AgentID != nil {
				role = agentNames[*child.AgentID]
			}
			s.Children = append(s.Children, workflow.Child{
				ID: child.ID, Title: child.Title, Instructions: child.Description,
				Type: child.TaskType, Role: role, Status: child.Status,
				ResultReason: child.ResultReason, ResultVerdict: child.ResultVerdict, ResultSummary: child.ResultSummary,
				ReviewRound: child.ReviewRound, OriginStepID: *child.OriginStepID,
				Question: child.OriginTool == workflow.ToolAskQuestions, DependsOn: child.DependsOn,
			})
		}
		if l.steps, err = d.q.ListTaskStepHeads(ctx, task.ID); err != nil {
			return nil, err
		}
		phaseStart := phaseStartStep(l.steps)
		for _, step := range l.steps {
			if step.Kind == models.StepHumanAnswer || step.Kind == models.StepRerun {
				s.SettledStepID = step.ID
				s.QuestionRounds = 0
			}
			if step.Kind != models.StepSmartCall {
				continue
			}
			if step.ID > phaseStart && step.ToolName == workflow.ToolAskQuestions {
				s.QuestionRounds++
			}
			if step.Phase == models.TaskPhaseAdjust && handlesFailures(step.ToolName) {
				s.HandledStepID = step.ID
			}
			if step.ID > phaseStart && workflow.IsInspectTool(step.ToolName) {
				s.InspectsUsed++
			}
		}
	}

	if task.RunID != nil {
		if run, err := d.q.GetRun(ctx, *task.RunID); err == nil {
			s.Run = workflowRun(run)
		}
		// A run that no longer exists is reported as nil: the session is gone.
	}

	if l.answer, err = d.q.FindHumanAnswer(ctx, task); err != nil {
		return nil, err
	}
	if l.answer != nil {
		s.HumanAnswer = &l.answer.Content
	}

	l.target, s.Model, s.ModelDetail = modelStatus(ctx, d.q, task)

	s.WorkspaceFree = true
	if task.Mode == models.TaskModeDirect && workflow.WritesWorkspace(task.TaskType) {
		root := task
		if task.RootTaskID != task.ID {
			if root, err = d.q.GetTask(ctx, task.RootTaskID); err != nil {
				return nil, err
			}
		}
		s.WorkspaceFree = root.WorkspaceOwnerTaskID == nil || *root.WorkspaceOwnerTaskID == task.ID
	}

	l.snapshot = s
	return l, nil
}

// handlesFailures reports whether an adjust-phase tool is a decision about
// what failed: after it, the failures that existed have been dealt with.
func handlesFailures(tool string) bool {
	switch tool {
	case workflow.ToolCreateTasks, workflow.ToolRetryTasks, workflow.ToolFinishAdjustment:
		return true
	}
	return false
}

// phaseStartStep is the ID of the step at which the task's current phase
// began: the last time it entered a phase, started, or was run again.
func phaseStartStep(steps []db.TaskStep) int64 {
	var start int64
	for _, step := range steps {
		switch step.Kind {
		case models.StepPhaseEntered, models.StepWorkflowStarted, models.StepRerun:
			start = step.ID
		}
	}
	return start
}

// workflowRun maps a stored executor session to the workflow's view of it.
func workflowRun(run db.Run) *workflow.Run {
	view := &workflow.Run{ID: run.ID, Error: strings.TrimSpace(run.LogContent)}
	switch run.Status {
	case "running", "waiting":
		view.Status = workflow.RunRunning
	case db.RunStatusPaused:
		view.Status = workflow.RunPaused
	case db.RunStatusResuming:
		view.Status = workflow.RunResuming
	case "completed":
		view.Status = workflow.RunCompleted
	case "canceled":
		view.Status = workflow.RunCanceled
	default:
		// failed, stale, and anything else that is not alive.
		view.Status = workflow.RunFailed
		view.VaultLocked = view.Error == vaultLockedDetail
		view.ModelFailure = strings.HasPrefix(view.Error, modelFailurePrefix)
	}
	if strings.TrimSpace(run.Report) != "" {
		var report workflow.Report
		if json.Unmarshal([]byte(run.Report), &report) == nil && report.Status != "" {
			view.Report = &report
		}
	}
	return view
}

// agentForRole finds the enabled agent that plays a role: a company's own
// agent with that role first, then the built-in one. It returns nil when the
// company has none.
func agentForRole(agents []db.Agent, role string) *db.Agent {
	if strings.TrimSpace(role) == "" {
		return nil
	}
	for _, builtin := range []bool{false, true} {
		for i := range agents {
			agent := &agents[i]
			if agent.Enabled && agent.Builtin == builtin && agentconfig.RoleMatches(agent.RoleKey, agent.Name, role) {
				return agent
			}
		}
	}
	return nil
}

func agentByID(agents []db.Agent, id *int32) *db.Agent {
	if id == nil {
		return nil
	}
	for i := range agents {
		if agents[i].ID == *id {
			return &agents[i]
		}
	}
	return nil
}

// speaker is the agent whose voice a task's step uses: the role the phase
// calls for if the company has it, otherwise the task's own agent, otherwise
// any enabled agent. A task is never left without a speaker because a role is
// missing; the role only shapes the prompt.
func speaker(agents []db.Agent, task db.Task, role string) *db.Agent {
	if agent := agentForRole(agents, role); agent != nil {
		return agent
	}
	if agent := agentByID(agents, task.AgentID); agent != nil && agent.Enabled {
		return agent
	}
	for i := range agents {
		if agents[i].Enabled {
			return &agents[i]
		}
	}
	return nil
}
