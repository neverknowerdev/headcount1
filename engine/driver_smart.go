package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/workflow"
)

// smartStep runs one stateless smart-model call for a task and returns the
// transition its answer causes. A non-nil error means the step was
// interrupted before it could produce anything to commit; a model that fails
// or answers unusably is not an error here but a transition of its own.
func (d *workflowDriver) smartStep(ctx context.Context, l *loadedTask, next workflow.Next, owner string) (*workflow.Transition, applyExtras, error) {
	task := l.task
	agent := speaker(l.agents, task, next.Role)
	extras := applyExtras{agent: agent}
	toolContext := l.snapshot.ToolContext(d.budgets)

	input, err := d.promptInput(ctx, l, next.Phase, agent)
	if err != nil {
		return nil, extras, err
	}
	// The lease is kept from here on: condensing a prompt that does not fit
	// calls models too, and the step must not lose the task meanwhile.
	callCtx, stopRenewing := d.keepLease(ctx, task.ID, owner)
	prompt := workflow.Compose(input)
	if len(prompt.Overflow) > 0 {
		prompt = d.condense(callCtx, l, next.Phase, agent, input, prompt)
	}
	request := aicli.ChatRequest{
		Messages: []aicli.Message{{Role: "system", Content: prompt.System}, {Role: "user", Content: prompt.User}},
	}
	for _, tool := range next.Tools {
		request.Tools = append(request.Tools, aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
			Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters,
		}})
	}
	promptJSON, _ := json.Marshal(request)
	extras.prompt = string(promptJSON)

	validate := func(call aicli.ToolCall) error {
		_, err := workflow.ParseAction(next.Phase, call.Function.Name, json.RawMessage(call.Function.Arguments), toolContext)
		return err
	}
	result, callErr := callOnce(callCtx, d.newClient, task.CompanyID, taskSession(task), l.target, request, validate)
	leaseLost := callCtx.Err() != nil && ctx.Err() == nil
	stopRenewing()

	usage := callContextFor(task, next.Phase, "step", agent)
	extras.usage = func(stepID *int64) {
		usage.StepID = stepID
		recordAttempts(context.Background(), d.q, usage, l.target, result, callErr == nil)
	}
	if result != nil && len(result.Attempts) > 0 {
		extras.response = string(result.Attempts[len(result.Attempts)-1].Raw)
	}

	if callErr != nil {
		if ctx.Err() != nil || leaseLost {
			// Shutting down, or no longer this driver's task: commit nothing.
			extras.recordUsage(nil)
			return nil, extras, fmt.Errorf("smart step interrupted: %w", callErr)
		}
		// A model that answered badly may do better next time; one that
		// could not be called at all is a different kind of trouble.
		kind := workflow.FailureModel
		message := callErr.Error()
		switch {
		case errors.Is(callErr, errVaultLocked):
			kind, message = workflow.FailureVaultLocked, vaultLockedDetail
		case errors.Is(callErr, aicli.ErrNoToolCall):
			kind = workflow.FailureTransient
		}
		return workflow.ApplyFailure(l.snapshot, d.budgets, kind, message), extras, nil
	}

	arguments := result.Call.Function.Arguments
	action, err := workflow.ParseAction(next.Phase, result.Call.Function.Name, json.RawMessage(arguments), toolContext)
	if err != nil {
		// The call passed this same check a moment ago; treat a disagreement
		// as a failed call rather than acting on an answer we cannot read.
		return workflow.ApplyFailure(l.snapshot, d.budgets, workflow.FailureTransient, err.Error()), extras, nil
	}
	switch inspect := action.(type) {
	case workflow.GetDecisionTree:
		extras.inspectOutput, err = d.decisionTree(ctx, task, inspect.Scope)
	case workflow.GetExecutionState:
		extras.inspectOutput, err = d.executionState(ctx, l, inspect.TaskIDs)
	}
	if err != nil {
		return workflow.ApplyFailure(l.snapshot, d.budgets, workflow.FailureTransient, err.Error()), extras, nil
	}
	return workflow.ApplyAction(l.snapshot, d.budgets, action, arguments), extras, nil
}

// decisionTree renders the decisions under a task, or under the whole tree it
// belongs to.
func (d *workflowDriver) decisionTree(ctx context.Context, task db.Task, scope string) (string, error) {
	top := task.ID
	if scope == "root" {
		top = task.RootTaskID
	}
	tasks, err := d.q.ListTaskSubtree(ctx, top)
	if err != nil {
		return "", err
	}
	all, err := d.q.ListDecisionsByRoot(ctx, task.RootTaskID)
	if err != nil {
		return "", err
	}
	inScope := make(map[int32]bool, len(tasks))
	for _, t := range tasks {
		inScope[t.ID] = true
	}
	decisions := all[:0:0]
	for _, decision := range all {
		if inScope[decision.TaskID] {
			decisions = append(decisions, decision)
		}
	}
	if len(decisions) == 0 {
		return "No decisions have been recorded yet.", nil
	}
	return renderDecisionTree(tasks, decisions), nil
}

// executionState renders subtasks in full, with the checkpoints their
// executors recorded.
func (d *workflowDriver) executionState(ctx context.Context, l *loadedTask, only []int32) (string, error) {
	ids := make([]int32, 0, len(l.children))
	for _, child := range l.children {
		ids = append(ids, child.ID)
	}
	checkpoints, err := d.q.ListStepsByKindForTasks(ctx, ids, models.StepCheckpoint)
	if err != nil {
		return "", err
	}
	return renderExecutionState(l.children, checkpoints, only), nil
}

// promptInput gathers everything the prompt for a task's next smart step is
// built from. It reads only stored state: the task, its journal, its
// subtasks' reports and the decisions made so far.
func (d *workflowDriver) promptInput(ctx context.Context, l *loadedTask, phase string, agent *db.Agent) (workflow.PromptInput, error) {
	task := l.task
	in := workflow.PromptInput{
		Phase:       phase,
		TaskRef:     task.RefKey,
		TaskTitle:   task.Title,
		TaskType:    task.TaskType,
		Description: task.Description,
		Spec:        task.RefinedDescription,
		Design:      task.Design,
		StepsLeft:   d.budgets.MaxSmartSteps - task.SmartStepsUsed,
		AdjustsLeft: d.budgets.MaxAdjustCycles - task.AdjustCyclesUsed,
	}
	if agent != nil {
		in.RolePrompt = agent.SystemPrompt
	}
	if attachments, err := d.q.ListAttachmentsByTask(ctx, treeRootID(task)); err == nil {
		for _, attachment := range attachments {
			in.Attachments = append(in.Attachments, attachment.Filename)
		}
	}
	_ = json.Unmarshal([]byte(task.AcceptanceCriteria), &in.DefinitionOfDone)
	_ = json.Unmarshal([]byte(task.TestCases), &in.TestScenarios)

	if task.ParentID != nil {
		if parent, err := d.q.GetTask(ctx, *task.ParentID); err == nil {
			name := parent.Title
			if parent.RefKey != "" {
				name = parent.RefKey + " — " + name
			}
			in.ParentContext = name
			if spec := strings.TrimSpace(parent.RefinedDescription); spec != "" {
				in.ParentContext += "\n" + oneLine(spec, 1500)
			}
		}
	}

	agentNames := make(map[int32]string, len(l.agents))
	for _, a := range l.agents {
		agentNames[a.ID] = a.Name
	}
	for i, child := range l.children {
		view := l.snapshot.Children[i]
		if view.Question {
			in.Answers = append(in.Answers, workflow.Answer{
				TaskID: child.ID, Question: child.Description, Kind: child.TaskType,
				Status: child.Status, Reason: child.ResultReason, Verdict: child.ResultVerdict, Summary: child.ResultSummary,
			})
			continue
		}
		report := workflow.SubtaskReport{
			TaskID: child.ID, RefKey: child.RefKey, Title: child.Title, Type: child.TaskType,
			Status: child.Status, Reason: child.ResultReason, Verdict: child.ResultVerdict, Summary: child.ResultSummary,
			DependsOn: child.DependsOn,
			Handled:   l.snapshot.HandledStepID > 0 && view.OriginStepID < l.snapshot.HandledStepID,
		}
		if child.AgentID != nil {
			report.Role = agentNames[*child.AgentID]
		}
		in.Subtasks = append(in.Subtasks, report)
	}

	phaseStart := phaseStartStep(l.steps)
	var pendingQuestion string
	for _, step := range l.steps {
		switch step.Kind {
		case models.StepHumanQuestion:
			pendingQuestion = step.Result
		case models.StepHumanAnswer:
			in.Humans = append(in.Humans, workflow.HumanExchange{Question: pendingQuestion, Answer: step.Result})
			pendingQuestion = ""
		case models.StepPhaseEntered, models.StepRerun:
			if step.ID == phaseStart && step.Phase == phase {
				in.Situation = step.Result
			}
		case models.StepSmartCall:
			if step.ID > phaseStart && workflow.IsInspectTool(step.ToolName) {
				in.Inspections = append(in.Inspections, workflow.Inspection{Tool: step.ToolName, Output: step.Result})
			}
		}
	}

	tree, err := d.decisionTree(ctx, task, "task")
	if err != nil {
		return in, err
	}
	if !strings.HasPrefix(tree, "No decisions") {
		in.Decisions = tree
	}
	return in, nil
}

// treeRootID is the task at the top of a task's tree.
func treeRootID(task db.Task) int32 {
	if task.RootTaskID != 0 {
		return task.RootTaskID
	}
	return task.ID
}
