package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/workflow"

	"gorm.io/gorm"
)

// errWorkspaceBusy means another task took the tree's worktree between the
// moment this task was read and the moment its step was applied.
var errWorkspaceBusy = errors.New("workflow: the shared worktree is held by another task")

// applyExtras is what the driver knows about a step that the pure transition
// cannot: the prompt and reply of a smart call, the output of a read-only
// tool, who spoke, and which comment answered a question.
type applyExtras struct {
	prompt        string
	response      string
	inspectOutput string
	agent         *db.Agent
	answer        *db.Comment
	// byHuman marks a change a person asked for, so the record of it says so.
	byHuman bool
	// usage writes the step's model calls to the ledger, linked to the
	// journal step if there is one.
	usage func(stepID *int64)
}

func (e applyExtras) recordUsage(stepID *int64) {
	if e.usage != nil {
		e.usage(stepID)
	}
}

// committedStep is what one applied transition wrote.
type committedStep struct {
	before    db.Task
	steps     []db.TaskStep
	decisions []db.Decision
	newTasks  []db.Task
	run       *db.Run
	comments  []db.Comment
	publishPR bool
	// releasedWorkspace is set when the step gave the tree's worktree back.
	releasedWorkspace bool
	// canceled lists the descendants the step canceled.
	canceled []int32
}

// orderDrafts sorts subtask drafts so each comes after the drafts it depends
// on, which lets them be created one by one with their edges.
func orderDrafts(drafts []workflow.TaskDraft) ([]workflow.TaskDraft, error) {
	index := make(map[string]int, len(drafts))
	for i, draft := range drafts {
		index[draft.Key] = i
	}
	ordered := make([]workflow.TaskDraft, 0, len(drafts))
	state := make([]int, len(drafts)) // 0 unvisited, 1 visiting, 2 done
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("subtask drafts form a dependency cycle through %q", drafts[i].Key)
		}
		state[i] = 1
		for _, key := range drafts[i].DependsOnKeys {
			j, ok := index[key]
			if !ok {
				return fmt.Errorf("subtask draft %q depends on unknown draft %q", drafts[i].Key, key)
			}
			if err := visit(j); err != nil {
				return err
			}
		}
		state[i] = 2
		ordered = append(ordered, drafts[i])
		return nil
	}
	for i := range drafts {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// runName is a session's human-readable key: its task, the role that runs
// it, and which attempt it is, as in "HC1-12-3-CODER-1".
func runName(task db.Task, agent db.Agent, attempt int) string {
	ref := task.RefKey
	if ref == "" {
		ref = fmt.Sprintf("TASK-%d", task.ID)
	}
	role := agent.ShortName
	if role == "" {
		role = agent.Name
	}
	return fmt.Sprintf("%s-%s-%d", ref, role, attempt)
}

// childWorkflowPhase is the phase of the top-level task a new subtask's cost
// is reported under. Below the top level it is inherited. At the top level it
// is the phase that created the subtask, except that work delegated by the
// plan belongs to execution: planning is the decision, the subtask is the
// doing.
func childWorkflowPhase(parent db.Task, question bool) string {
	if parent.ParentID != nil && parent.WorkflowPhase != "" {
		return parent.WorkflowPhase
	}
	if !question && parent.Phase == models.TaskPhasePlan {
		return models.TaskPhaseExecute
	}
	return parent.Phase
}

// apply persists a transition: every write it implies, in one transaction,
// guarded by the task's lease and by the state the decision was based on.
func (d *workflowDriver) apply(ctx context.Context, l *loadedTask, tr *workflow.Transition, extras applyExtras, owner string) (*committedStep, error) {
	task := l.task
	drafts, err := orderDrafts(tr.NewTasks)
	if err != nil {
		return nil, err
	}
	var runAgent *db.Agent
	if tr.StartRun {
		if runAgent = speaker(l.agents, task, ""); runAgent == nil {
			return nil, fmt.Errorf("task %d has no enabled agent to run as", task.ID)
		}
	}
	var speakerID *int32
	if extras.agent != nil {
		id := extras.agent.ID
		speakerID = &id
	}
	now := d.now()
	c := &committedStep{before: task, publishPR: tr.PublishPR}

	err = d.q.WorkflowTransition(ctx, db.GuardFor(task, owner), func(tx *db.WorkflowTx) error {
		// Start from a clean slate: a retried transaction must not keep rows
		// from the attempt that was rolled back.
		*c = committedStep{before: task, publishPR: tr.PublishPR}
		fields := make(map[string]interface{}, len(tr.Fields)+2)
		for column, value := range tr.Fields {
			if inc, ok := value.(workflow.Inc); ok {
				fields[column] = gorm.Expr(column+" + ?", int(inc))
				continue
			}
			fields[column] = value
		}

		var question *db.Comment
		if tr.AskHuman != "" {
			// Questions go on the root task: that is the one the human watches.
			comment, err := tx.CreateComment(db.Comment{
				TaskID: task.RootTaskID, AuthorType: "agent", AuthorID: speakerID,
				CommentType: "ask_user", Content: tr.AskHuman, CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				return err
			}
			question = &comment
			c.comments = append(c.comments, comment)
			fields["wait_ref"] = comment.ID
		}

		if tr.StartRun {
			run, err := tx.CreateRun(db.Run{
				TaskID: task.ID, AgentID: runAgent.ID, Status: "running",
				Name:      runName(task, *runAgent, task.AttemptsUsed+1),
				Title:     task.Title,
				StartedAt: now, LastMessageTime: &now, Attempt: task.AttemptsUsed + 1,
			})
			if err != nil {
				return err
			}
			c.run = &run
			fields["run_id"] = run.ID
		}

		for _, step := range tr.Steps {
			row := db.TaskStep{
				Kind: step.Kind, Phase: step.Phase, AgentID: speakerID,
				ToolName: step.ToolName, ToolArgs: step.ToolArgs,
				Result: step.Result, Error: step.Error, RefTaskID: step.RefTaskID, RunID: step.RunID,
			}
			switch step.Kind {
			case models.StepSmartCall:
				row.Prompt, row.Response = extras.prompt, extras.response
				if extras.inspectOutput != "" {
					row.Result = extras.inspectOutput
				}
			case models.StepHumanQuestion:
				if question != nil {
					row.CommentID = &question.ID
				}
			case models.StepHumanAnswer:
				if extras.answer != nil {
					row.CommentID = &extras.answer.ID
				}
			case models.StepRunStarted:
				if c.run != nil {
					row.RunID = &c.run.ID
				}
			}
			saved, err := tx.AppendStep(row)
			if err != nil {
				return err
			}
			c.steps = append(c.steps, saved)
		}

		// Decisions and subtasks hang off the step that made them.
		var origin *int64
		if len(c.steps) > 0 {
			origin = &c.steps[0].ID
		}
		asksQuestions := len(tr.Steps) > 0 && tr.Steps[0].ToolName == workflow.ToolAskQuestions

		for _, decision := range tr.Decisions {
			alternatives := ""
			if len(decision.Alternatives) > 0 {
				encoded, _ := json.Marshal(decision.Alternatives)
				alternatives = string(encoded)
			}
			row := db.Decision{
				StepID: origin, Phase: task.Phase, AgentID: speakerID, Kind: decision.Kind(),
				Title: decision.Title, Decision: decision.Decision, Rationale: decision.Rationale, Alternatives: alternatives,
			}
			if decision.ParentID > 0 {
				parent := decision.ParentID
				row.ParentDecisionID = &parent
			}
			saved, err := tx.AddDecision(row)
			if err != nil {
				return err
			}
			c.decisions = append(c.decisions, saved)
		}

		created := make(map[string]int32, len(drafts))
		for _, draft := range drafts {
			agent := agentForRole(l.agents, draft.Role)
			if agent == nil {
				agent = speaker(l.agents, task, "")
			}
			child := db.Task{
				Title: draft.Title, Description: draft.Instructions,
				TaskType: draft.Type, Mode: draft.Mode, Status: models.TaskStatusTodo,
				OriginStepID: origin, OriginPhase: task.Phase,
				WorkflowPhase: childWorkflowPhase(task, asksQuestions),
				ReviewRound:   draft.ReviewRound, Priority: task.Priority,
			}
			if agent != nil {
				id := agent.ID
				child.AgentID = &id
			}
			dependsOn := append([]int32(nil), draft.DependsOnIDs...)
			for _, key := range draft.DependsOnKeys {
				dependsOn = append(dependsOn, created[key])
			}
			saved, err := tx.CreateSubtask(child, dependsOn)
			if err != nil {
				return err
			}
			created[draft.Key] = saved.ID
			c.newTasks = append(c.newTasks, saved)
		}

		if tr.CancelDescendants != "" {
			canceled, err := tx.CancelDescendants(tr.CancelDescendants)
			if err != nil {
				return err
			}
			c.canceled = canceled
		}
		if tr.ClaimWorkspace {
			claimed, err := tx.ClaimWorkspace()
			if err != nil {
				return err
			}
			if !claimed {
				return errWorkspaceBusy
			}
		}
		if tr.ReleaseWorkspace {
			if err := tx.ReleaseWorkspace(); err != nil {
				return err
			}
			c.releasedWorkspace = true
		}

		if status, ok := tr.Fields["status"].(string); ok && status != task.Status {
			content, _ := json.Marshal(map[string]string{"from": task.Status, "to": status})
			author := "system"
			if extras.byHuman {
				author = "human"
			}
			comment, err := tx.CreateComment(db.Comment{
				TaskID: task.ID, AuthorType: author, CommentType: "status_change",
				Content: string(content), CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				return err
			}
			c.comments = append(c.comments, comment)
		}
		return tx.UpdateTask(fields)
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// afterCommit does what follows from a committed step: tells the UI, mirrors
// the journal to disk, and wakes every task the change may concern. None of
// it is needed for correctness — a wake-up lost here is found by the sweeper
// — so nothing in it can fail the step.
func (d *workflowDriver) afterCommit(ctx context.Context, c *committedStep, extras applyExtras) {
	var stepID *int64
	for i := range c.steps {
		if c.steps[i].Kind == models.StepSmartCall || c.steps[i].Kind == models.StepSmartError {
			stepID = &c.steps[i].ID
			break
		}
	}
	extras.recordUsage(stepID)

	task, err := d.q.GetTask(ctx, c.before.ID)
	if err != nil {
		fmt.Printf("Warning: workflow could not re-read task %d after a step: %v\n", c.before.ID, err)
		task = c.before
	}
	mirrorJournal(d.basePath(), task.Company.ShortName, task, c.steps, c.decisions)

	d.broadcast(task.CompanyID, "task_updated", task)
	for _, step := range c.steps {
		d.broadcast(task.CompanyID, "task_step", map[string]interface{}{"task_id": task.ID, "step": step})
	}
	for _, comment := range c.comments {
		d.broadcast(task.CompanyID, "comment_created", comment)
	}
	for _, child := range c.newTasks {
		d.broadcast(task.CompanyID, "task_created", child)
		d.Enqueue(child.ID)
	}

	if task.Status != c.before.Status && models.IsTerminalTaskStatus(task.Status) {
		// Whoever was waiting on this task can now move.
		if task.ParentID != nil {
			d.Enqueue(*task.ParentID)
		}
		if dependents, err := d.q.ListDependentTasks(ctx, task.ID); err == nil {
			for _, dependent := range dependents {
				d.Enqueue(dependent.ID)
			}
		}
	}
	if c.releasedWorkspace {
		// The worktree is free: the writers queued for it can try again now
		// rather than at the next sweep.
		if waiting, err := d.q.ListTasksWaitingOn(ctx, task.RootTaskID, models.TaskWaitWorkspace); err == nil {
			for _, id := range waiting {
				d.Enqueue(id)
			}
		}
	}
	if task.WaitingOn == models.TaskWaitBackoff && task.WaitUntil != nil {
		d.EnqueueAfter(task.ID, task.WaitUntil.Sub(d.now()))
	}
	if c.run != nil {
		d.startExecutor(task, *c.run)
	}
	if c.publishPR {
		d.publishPR(task)
	}
}
