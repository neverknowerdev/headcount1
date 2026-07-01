package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/logging"
)

// AskModeOrchestrator executes tasks through the 3-stage pipeline:
// refinement → implementation → testing.
type AskModeOrchestrator struct {
	q            *db.Queries
	hub          *eventhub.Hub
	agentFactory agentconfig.Factory
	settings     Settings
	// processTask, when non-nil, is called after a subtask is created by the
	// Coder so the engine can enqueue it for execution.
	processTask func(ctx context.Context, taskID int32) error
}

// newAskModeOrchestrator creates an AskModeOrchestrator for one task execution.
func newAskModeOrchestrator(q *db.Queries, hub *eventhub.Hub, factory agentconfig.Factory, settings Settings) *AskModeOrchestrator {
	return &AskModeOrchestrator{q: q, hub: hub, agentFactory: factory, settings: settings}
}

// ExecuteTask runs the full 3-stage pipeline for a task.
// Returns (true, nil) when the pipeline succeeds and the task status has been
// updated; (false, err) on failure.
func (o *AskModeOrchestrator) ExecuteTask(
	ctx context.Context,
	task db.Task,
	run db.Run,
	workspacePath string,
	logger *logging.ProxyLogger,
) (taskFinished bool, err error) {
	o.logInfo(logger, "AskMode: starting 3-stage pipeline (refinement → implementation → testing)")

	// Validate that every role this task will need has a resolvable
	// provider/model *before* doing any work — a role misconfigured deep in
	// the pipeline (e.g. Coder) shouldn't be discovered only after refinement
	// has already run.
	if valErr := o.validateRoleModels(ctx, task); valErr != nil {
		return false, valErr
	}

	// Create the main orchestrator session.
	session, sessionErr := o.q.CreateSession(ctx, db.Session{
		TaskID:          task.ID,
		SessionType:     "orchestration",
		AgentConfigName: "SmartPlanner",
		Status:          "running",
	})
	if sessionErr != nil {
		return false, fmt.Errorf("create orchestrator session: %w", sessionErr)
	}
	o.q.SetTaskMainSession(ctx, task.ID, session.ID) //nolint:errcheck

	// ── Stage 1: Refinement ──────────────────────────────────────────────────
	o.logInfo(logger, "AskMode: stage 1 — refinement")
	refinement, refineErr := o.runRefinement(ctx, task, session, workspacePath, logger)
	if refineErr != nil {
		o.q.UpdateSessionResult(ctx, session.ID, "failed", refineErr.Error()) //nolint:errcheck
		return false, fmt.Errorf("refinement failed: %w", refineErr)
	}
	o.logInfo(logger, "AskMode: refinement complete")

	// Persist refinement output to the task.
	task.DetailedDescription = refinement.DetailedDescription
	task.Specifications = refinement.Specifications
	task.AcceptanceCriteria = refinement.AcceptanceCriteria
	task.TestCases = refinement.TestCases
	if task, err = o.q.UpdateTask(ctx, task); err != nil {
		return false, fmt.Errorf("save refinement to task: %w", err)
	}

	// ── Stages 2 & 3: Implementation → Testing, up to 3 cycles ─────────────
	escalationCh := make(chan tools.Escalation, 1)

	for cycle := 1; cycle <= 3; cycle++ {
		o.logInfo(logger, fmt.Sprintf("AskMode: stage 2 — implementation (cycle %d/3)", cycle))

		implErr := o.runSubAgent(ctx, "Coder", "implementation", task, session, workspacePath, logger, escalationCh, run)
		if implErr != nil {
			o.logInfo(logger, fmt.Sprintf("AskMode: implementation cycle %d failed: %v", cycle, implErr))
			if cycle == 3 {
				o.q.UpdateSessionResult(ctx, session.ID, "failed", fmt.Sprintf("implementation: %v", implErr)) //nolint:errcheck
				return false, fmt.Errorf("implementation failed after 3 cycles: %w", implErr)
			}
			continue
		}

		o.logInfo(logger, fmt.Sprintf("AskMode: stage 3 — testing (cycle %d/3)", cycle))

		testErr := o.runSubAgent(ctx, "Tester", "testing", task, session, workspacePath, logger, escalationCh, run)
		if testErr == nil {
			// Mark task as in-review and create the done comment.
			o.finalizeTask(ctx, task, run)
			o.q.UpdateSessionResult(ctx, session.ID, "completed", "Task completed successfully") //nolint:errcheck
			return true, nil
		}

		o.logInfo(logger, fmt.Sprintf("AskMode: testing cycle %d failed: %v", cycle, testErr))
		if cycle == 3 {
			o.q.UpdateSessionResult(ctx, session.ID, "failed", fmt.Sprintf("testing: %v", testErr)) //nolint:errcheck
			return false, fmt.Errorf("testing failed after 3 cycles: %w", testErr)
		}
	}

	return false, fmt.Errorf("task failed after 3 implementation/testing cycles")
}

// finalizeTask moves the task to in-review and creates the completion comment.
func (o *AskModeOrchestrator) finalizeTask(ctx context.Context, task db.Task, run db.Run) {
	t, err := o.q.GetTask(ctx, task.ID)
	if err != nil {
		return
	}
	prevStatus := t.Status
	t.Status = "in-review"
	if _, err := o.q.UpdateTask(ctx, t); err != nil {
		return
	}
	o.hub.BroadcastEvent("task_updated", map[string]interface{}{"id": task.ID, "status": "in-review"})
	o.emitStatusChange(ctx, task.ID, prevStatus, "in-review")

	runID := run.ID
	content, _ := json.Marshal(map[string]string{
		"msg":  "Task completed by AskMode pipeline",
		"from": prevStatus,
		"to":   "in-review",
	})
	comment, cErr := o.q.CreateComment(ctx, db.Comment{
		TaskID:      task.ID,
		AuthorType:  "agent",
		CommentType: "task_done",
		Content:     string(content),
		RunID:       &runID,
	})
	if cErr == nil {
		o.hub.BroadcastEvent("comment_created", comment)
	}
}

// emitStatusChange broadcasts and persists a status-change comment.
func (o *AskModeOrchestrator) emitStatusChange(ctx context.Context, taskID int32, from, to string) {
	content, _ := json.Marshal(map[string]string{"from": from, "to": to})
	comment, err := o.q.CreateComment(ctx, db.Comment{
		TaskID:      taskID,
		AuthorType:  "system",
		CommentType: "status_change",
		Content:     string(content),
	})
	if err == nil {
		o.hub.BroadcastEvent("comment_created", comment)
	}
}

// runRefinement runs the SmartPlanner agent with AskQuestionBatcher.
// Returns the structured output from finish_refinement.
func (o *AskModeOrchestrator) runRefinement(
	ctx context.Context,
	task db.Task,
	session db.Session,
	workspacePath string,
	logger *logging.ProxyLogger,
) (tools.FinishRefinementArgs, error) {
	provider, model, err := o.resolveRoleProvider(ctx, "SmartPlanner")
	if err != nil {
		return tools.FinishRefinementArgs{}, err
	}

	// Capture finish_refinement result via buffered channel.
	finishCh := make(chan tools.FinishRefinementArgs, 1)
	refinementCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	registry := aicli.NewRegistry()
	registry.Register(tools.NewAskQuestion())
	registry.Register(tools.NewFinishRefinement(func(_ context.Context, args tools.FinishRefinementArgs) error {
		finishCh <- args
		cancel() // stop the planner loop after capturing the result
		return nil
	}))
	registry.Register(tools.NewExpandRunResult(func(rCtx context.Context, runID int32) (string, error) {
		r, err := o.q.GetRun(rCtx, runID)
		if err != nil {
			return "", err
		}
		return r.ResultDescription, nil
	}))

	// Wire AskQuestionBatcher: each batch of questions spawns one researcher sub-session.
	batcher := &aicli.AskQuestionBatcher{
		RunBatch: func(bCtx context.Context, questions []string) ([]string, error) {
			return o.runResearchBatch(bCtx, task, session.ID, workspacePath, questions, logger)
		},
	}

	agentCfg, _ := o.agentFactory.GetConfig("SmartPlanner")
	client := aicli.NewClient(provider.BaseUrl, provider.ApiKey, model)
	plannerAgent := aicli.New(aicli.Config{
		Client:             client,
		Registry:           registry,
		ProviderName:       provider.Name,
		AgentName:          "SmartPlanner",
		ReasoningLevel:     o.reasoningLevel(agentCfg),
		AskQuestionBatcher: batcher,
		Queries:            o.q,
		RunID:              0,
		Logger:             logger,
	})

	var systemPrompt string
	if agentCfg != nil && agentCfg.Prompt != "" {
		systemPrompt = agentCfg.Prompt
	}

	_, runErr := plannerAgent.Run(refinementCtx, systemPrompt, o.buildRefinementMessage(task))
	// Context cancellation by finish_refinement callback is expected and not an error.
	if runErr != nil && refinementCtx.Err() == nil {
		return tools.FinishRefinementArgs{}, fmt.Errorf("SmartPlanner failed: %w", runErr)
	}

	select {
	case result := <-finishCh:
		return result, nil
	default:
		return tools.FinishRefinementArgs{}, fmt.Errorf("SmartPlanner did not call finish_refinement")
	}
}

// runResearchBatch creates a researcher sub-session with all batched questions.
// Returns ordered answers indexed by question position (0-based).
func (o *AskModeOrchestrator) runResearchBatch(
	ctx context.Context,
	task db.Task,
	parentSessionID int32,
	workspacePath string,
	questions []string,
	logger *logging.ProxyLogger,
) ([]string, error) {
	configName := o.researcherConfigName(task.TaskType)
	provider, model, err := o.resolveRoleProvider(ctx, configName)
	if err != nil {
		return nil, err
	}

	psID := parentSessionID
	subSession, err := o.q.CreateSession(ctx, db.Session{
		TaskID:          task.ID,
		ParentSessionID: &psID,
		SessionType:     "qa-research",
		AgentConfigName: configName,
		Status:          "running",
	})
	if err != nil {
		return nil, fmt.Errorf("create researcher session: %w", err)
	}

	// Build numbered question prompt.
	var sb strings.Builder
	sb.WriteString("Please research and answer the following questions:\n\n")
	for i, q := range questions {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, q)
	}

	// researchCtx is canceled as soon as answer_question is called, so the
	// researcher's agent loop stops immediately instead of making another
	// (unnecessary) LLM turn.
	researchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	completeCh := make(chan []tools.QuestionAnswer, 1)
	registry := tools.DefaultRegistry(workspacePath)
	registry.Register(tools.NewSubAgentAnswer(completeCh, cancel))

	agentCfg, _ := o.agentFactory.GetConfig(configName)
	if agentCfg != nil && len(agentCfg.AllowedTools) > 0 {
		registry = registry.Filter(agentCfg.AllowedTools)
	}

	var systemPrompt string
	if agentCfg != nil && agentCfg.Prompt != "" {
		systemPrompt = agentCfg.Prompt
	}

	client := aicli.NewClient(provider.BaseUrl, provider.ApiKey, model)
	researchAgent := aicli.New(aicli.Config{
		Client:         client,
		Registry:       registry,
		ProviderName:   provider.Name,
		AgentName:      configName,
		ReasoningLevel: o.reasoningLevel(agentCfg),
		Logger:         logger,
	})

	doneCh := make(chan error, 1)
	go func() {
		_, runErr := researchAgent.Run(researchCtx, systemPrompt, sb.String())
		doneCh <- runErr
	}()

	// Wait for answer_question call or agent exit.
	select {
	case qaList := <-completeCh:
		o.q.UpdateSessionResult(ctx, subSession.ID, "completed", fmt.Sprintf("answered %d questions", len(qaList))) //nolint:errcheck
		answers := o.qaListToAnswers(qaList, len(questions))
		<-doneCh // drain goroutine
		return answers, nil

	case runErr := <-doneCh:
		// Non-blocking check: agent may have answered just as it exited.
		select {
		case qaList := <-completeCh:
			o.q.UpdateSessionResult(ctx, subSession.ID, "completed", fmt.Sprintf("answered %d questions", len(qaList))) //nolint:errcheck
			return o.qaListToAnswers(qaList, len(questions)), nil
		default:
		}
		o.q.UpdateSessionResult(ctx, subSession.ID, "failed", "exited without answering") //nolint:errcheck
		if runErr != nil {
			return nil, fmt.Errorf("researcher %s failed: %w", configName, runErr)
		}
		return nil, fmt.Errorf("researcher %s exited without calling answer_question", configName)

	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// qaListToAnswers converts a []QuestionAnswer (1-based) to a 0-based answers slice.
func (o *AskModeOrchestrator) qaListToAnswers(qaList []tools.QuestionAnswer, count int) []string {
	answers := make([]string, count)
	for _, qa := range qaList {
		if qa.N >= 1 && qa.N <= count {
			answers[qa.N-1] = qa.Answer
		}
	}
	return answers
}

// runSubAgent runs either the Coder or Tester agent, handling escalations in a
// select loop until the sub-agent finishes.
func (o *AskModeOrchestrator) runSubAgent(
	ctx context.Context,
	configName, sessionType string,
	task db.Task,
	parentSession db.Session,
	workspacePath string,
	logger *logging.ProxyLogger,
	escalationCh chan tools.Escalation,
	run db.Run,
) error {
	provider, model, err := o.resolveRoleProvider(ctx, configName)
	if err != nil {
		return fmt.Errorf("resolve provider for %s: %w", configName, err)
	}

	psID := parentSession.ID
	subSession, err := o.q.CreateSession(ctx, db.Session{
		TaskID:          task.ID,
		ParentSessionID: &psID,
		SessionType:     sessionType,
		AgentConfigName: configName,
		Status:          "running",
	})
	if err != nil {
		return fmt.Errorf("create %s session: %w", configName, err)
	}

	registry := tools.DefaultRegistry(workspacePath)
	registry.Register(tools.NewAskTaskOwner(subSession.ID, escalationCh, nil))

	// Capture finish_task call from sub-agent.
	var subTaskStatus string
	registry.Register(tools.NewFinishTask(func(_ context.Context, status, _ string) error {
		subTaskStatus = status
		return nil
	}))

	// Allow the Coder to create subtasks if the engine provided a processor.
	if o.processTask != nil {
		var agentNames []string
		if o.agentFactory != nil {
			agentNames = o.agentFactory.ListNames()
		}
		createFn := func(callCtx context.Context, title, description, agentName string) (int32, error) {
			if o.agentFactory != nil {
				if _, cfgErr := o.agentFactory.GetConfig(agentName); cfgErr != nil {
					return 0, fmt.Errorf("unknown agent config %q: %w", agentName, cfgErr)
				}
			}
			// Check for already-running subtask.
			running, countErr := o.q.CountRunningSubtasks(callCtx, task.ID)
			if countErr != nil {
				return 0, fmt.Errorf("check running subtasks: %w", countErr)
			}
			if running > 0 {
				return 0, fmt.Errorf("a subtask is already running for task %d; wait for it to complete before creating another", task.ID)
			}
			parentID := task.ID
			sub, createErr := o.q.CreateTask(callCtx, db.Task{
				CompanyID:       task.CompanyID,
				ProjectID:       task.ProjectID,
				SprintID:        task.SprintID,
				AgentID:         task.AgentID,
				ParentID:        &parentID,
				Title:           title,
				Description:     description,
				TaskType:        db.TaskTypeTech,
				Status:          "to-do",
				Priority:        "Normal",
				AgentConfigName: agentName,
			})
			if createErr != nil {
				return 0, fmt.Errorf("create subtask: %w", createErr)
			}
			o.hub.BroadcastEvent("task_created", map[string]interface{}{
				"id": sub.ID, "parent_id": task.ID, "title": sub.Title,
			})
			if procErr := o.processTask(callCtx, sub.ID); procErr != nil {
				return sub.ID, fmt.Errorf("enqueue subtask: %w", procErr)
			}
			return sub.ID, nil
		}
		registry.Register(tools.NewCreateSubtask(createFn, agentNames))
	}

	agentCfg, _ := o.agentFactory.GetConfig(configName)
	if agentCfg != nil && len(agentCfg.AllowedTools) > 0 {
		registry = registry.Filter(agentCfg.AllowedTools)
	}

	var systemPrompt string
	if agentCfg != nil && agentCfg.Prompt != "" {
		systemPrompt = agentCfg.Prompt
	}
	systemPrompt += "\n\n" + o.buildSubAgentContext(task, sessionType)

	client := aicli.NewClient(provider.BaseUrl, provider.ApiKey, model)
	agent := aicli.New(aicli.Config{
		Client:         client,
		Registry:       registry,
		ProviderName:   provider.Name,
		AgentName:      configName,
		ReasoningLevel: o.reasoningLevel(agentCfg),
		Queries:        o.q,
		RunID:          run.ID,
		Logger:         logger,
	})

	doneCh := make(chan error, 1)
	go func() {
		_, runErr := agent.Run(ctx, systemPrompt, o.buildSubAgentMessage(task, sessionType))
		doneCh <- runErr
	}()

	// Handle escalations until sub-agent finishes.
	for {
		select {
		case escalation := <-escalationCh:
			answer, escalErr := o.handleEscalation(ctx, task, escalation.Question, logger)
			if escalErr != nil {
				escalation.AnswerCh <- fmt.Sprintf("Error answering: %v", escalErr)
			} else {
				escalation.AnswerCh <- answer
			}

		case runErr := <-doneCh:
			if runErr != nil {
				o.q.UpdateSessionResult(ctx, subSession.ID, "failed", runErr.Error()) //nolint:errcheck
				return runErr
			}
			if subTaskStatus == "blocked" {
				msg := fmt.Sprintf("%s: blocked", configName)
				o.q.UpdateSessionResult(ctx, subSession.ID, "failed", msg) //nolint:errcheck
				return fmt.Errorf("%s", msg)
			}
			o.q.UpdateSessionResult(ctx, subSession.ID, "completed", fmt.Sprintf("%s: %s", configName, subTaskStatus)) //nolint:errcheck
			return nil

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// handleEscalation asks the SmartPlanner to answer a question from a sub-agent.
// Uses a one-shot LLM call with the full task context.
func (o *AskModeOrchestrator) handleEscalation(
	ctx context.Context,
	task db.Task,
	question string,
	logger *logging.ProxyLogger,
) (string, error) {
	o.logInfo(logger, fmt.Sprintf("AskMode: escalation from sub-agent: %q", truncate(question, 80)))

	provider, model, err := o.resolveRoleProvider(ctx, "SmartPlanner")
	if err != nil {
		return "", err
	}

	prompt := fmt.Sprintf(
		"You are the task orchestrator. An implementer has a question that requires your decision.\n\n"+
			"Task: %s\n\nDetailed Description:\n%s\n\nSpecifications:\n%s\n\nAcceptance Criteria:\n%s\n\n"+
			"Question from implementer:\n%s\n\nProvide a clear, specific answer so the implementer can proceed.",
		task.Title, task.DetailedDescription, task.Specifications, task.AcceptanceCriteria, question,
	)

	client := aicli.NewClient(provider.BaseUrl, provider.ApiKey, model)
	resp, _, err := client.Complete(ctx, aicli.ChatRequest{
		Messages:  []aicli.Message{{Role: "user", Content: prompt}},
		MaxTokens: 1000,
	})
	if err != nil {
		return "", fmt.Errorf("escalation LLM call failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("escalation returned no answer")
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	o.logInfo(logger, fmt.Sprintf("AskMode: escalation answered: %q", truncate(answer, 80)))
	return answer, nil
}

// validateRoleModels checks that every role this task's pipeline will invoke
// (SmartPlanner, the task-type researcher, Coder, Tester) resolves to a
// usable provider and model, returning a single combined error naming every
// misconfigured role if not. This is the only case that should ever surface
// as "no model set for role" — as long as at least one LLM provider exists,
// every role should resolve to something.
func (o *AskModeOrchestrator) validateRoleModels(ctx context.Context, task db.Task) error {
	roles := []string{"SmartPlanner", o.researcherConfigName(task.TaskType), "Coder", "Tester"}
	var problems []string
	for _, role := range roles {
		if _, _, err := o.resolveRoleProvider(ctx, role); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", role, err))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("no model configured for role(s) — set a provider for each in Role Model Configuration (LLM Providers page): %s", strings.Join(problems, "; "))
	}
	return nil
}

// resolveRoleProvider looks up the LLM provider and model for a given agent role.
// Falls back to the system-wide default provider (Settings.DefaultProviderID)
// if the role is not explicitly configured, and to the first available
// provider as a last-resort safety net if no default provider is set either.
func (o *AskModeOrchestrator) resolveRoleProvider(ctx context.Context, configName string) (db.LLMProvider, string, error) {
	rm := o.settings.RoleModels
	var providerID int32
	var model string

	switch configName {
	case "SmartPlanner":
		providerID, model = rm.SmartPlannerProviderID, rm.SmartPlannerModel
	case "TechSpecResearcher":
		providerID, model = rm.TechResearcherProviderID, rm.TechResearcherModel
	case "WritingSpecResearcher":
		providerID, model = rm.WritingResearcherProviderID, rm.WritingResearcherModel
	case "DesignSpecResearcher":
		providerID, model = rm.DesignResearcherProviderID, rm.DesignResearcherModel
	case "Coder":
		providerID, model = rm.CoderProviderID, rm.CoderModel
	case "Tester":
		providerID, model = rm.TesterProviderID, rm.TesterModel
	}

	if providerID == 0 {
		providerID = o.settings.DefaultProviderID
	}

	if providerID == 0 {
		providers, listErr := o.q.ListLLMProviders(ctx)
		if listErr != nil || len(providers) == 0 {
			return db.LLMProvider{}, "", fmt.Errorf("no LLM providers available for role %s", configName)
		}
		p := providers[0]
		if model == "" {
			model = p.DefaultModel
		}
		return p, model, nil
	}

	p, getErr := o.q.GetLLMProvider(ctx, providerID)
	if getErr != nil {
		return db.LLMProvider{}, "", fmt.Errorf("get provider %d for %s: %w", providerID, configName, getErr)
	}
	if model == "" {
		model = p.DefaultModel
	}
	return p, model, nil
}

func (o *AskModeOrchestrator) researcherConfigName(taskType string) string {
	switch taskType {
	case db.TaskTypeWriting:
		return "WritingSpecResearcher"
	case db.TaskTypeDesign:
		return "DesignSpecResearcher"
	default:
		return "TechSpecResearcher"
	}
}

func (o *AskModeOrchestrator) reasoningLevel(cfg *agentconfig.AgentConfig) string {
	if cfg == nil {
		return ""
	}
	return string(cfg.ReasoningLevel)
}

func (o *AskModeOrchestrator) buildRefinementMessage(task db.Task) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Title: %s\n", task.Title)
	if task.Description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", task.Description)
	}
	if task.UserInput != "" {
		fmt.Fprintf(&sb, "User Input: %s\n", task.UserInput)
	}
	sb.WriteString("\nPlease refine this task by researching the codebase and creating detailed specifications.")
	return sb.String()
}

func (o *AskModeOrchestrator) buildSubAgentContext(task db.Task, sessionType string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Task: %s\n\n", task.Title)
	if task.DetailedDescription != "" {
		fmt.Fprintf(&sb, "Detailed Description:\n%s\n\n", task.DetailedDescription)
	}
	if task.Specifications != "" {
		fmt.Fprintf(&sb, "Specifications:\n%s\n\n", task.Specifications)
	}
	if task.AcceptanceCriteria != "" {
		fmt.Fprintf(&sb, "Acceptance Criteria:\n%s\n\n", task.AcceptanceCriteria)
	}
	if task.TestCases != "" && sessionType == "testing" {
		fmt.Fprintf(&sb, "Test Cases:\n%s\n\n", task.TestCases)
	}
	return strings.TrimSpace(sb.String())
}

func (o *AskModeOrchestrator) buildSubAgentMessage(task db.Task, sessionType string) string {
	switch sessionType {
	case "testing":
		return fmt.Sprintf("Please test the implementation of task '%s' against the acceptance criteria.", task.Title)
	default:
		return fmt.Sprintf("Please implement task '%s' according to the specifications.", task.Title)
	}
}

func (o *AskModeOrchestrator) logInfo(logger *logging.ProxyLogger, msg string) {
	if logger == nil {
		fmt.Println(msg)
		return
	}
	logger.LogInfo(msg)
}

// truncate shortens s for log display, appending … when trimmed.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
