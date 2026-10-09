package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"agent-orchestrator/db/models"
)

// Task is the part of a task the workflow decides on.
type Task struct {
	ID     int32
	IsRoot bool
	Depth  int
	Type   string
	// ParentType is the type of the task this one is a subtask of.
	ParentType string
	Mode       string
	Status     string
	Phase      string

	WaitingOn string
	WaitUntil *time.Time

	SmartStepsUsed   int
	AdjustCyclesUsed int
	// AttemptsUsed counts executor sessions for a direct task and consecutive
	// failed smart calls for a managed one.
	AttemptsUsed int
	ReviewRound  int
}

// Child is a subtask this task's own steps created.
type Child struct {
	ID            int32
	Title         string
	Instructions  string
	Type          string
	Role          string
	Status        string
	ResultReason  string
	ResultVerdict string
	ResultSummary string
	ReviewRound   int
	// OriginStepID is the parent's step that created the child. Question is
	// true when that step asked a question rather than delegated work.
	OriginStepID int64
	Question     bool
	// DependsOn lists the siblings this child waits for.
	DependsOn []int32
}

// Prerequisite is a task another one depends on.
type Prerequisite struct {
	Status      string
	Type        string
	Verdict     string
	ReviewRound int
}

// requestedChanges reports whether a finished review asked for changes. Its
// own work succeeded, but what it reviewed has not been accepted.
func requestedChanges(taskType, status, verdict string) bool {
	return taskType == models.TaskTypeReview && status == models.TaskStatusDone && verdict == models.TaskVerdictChangesRequested
}

// answersReview reports whether a task is the fix for a review of the given
// round: the one task that starts because the review asked for changes.
func answersReview(taskType string, reviewRound, ofReviewRound int) bool {
	return taskType == models.TaskTypeCoding && reviewRound == ofReviewRound+1
}

// fixRoundFollows reports whether the engine answers a review that asked for
// changes with a fix and a new review of its own: only where the parent's
// workflow reviews implementation, and only up to the limit of rounds.
func fixRoundFollows(parentType string, reviewRound int, b Budgets) bool {
	return TemplateFor(parentType).ReviewImplementation && reviewRound < b.MaxReviewRounds
}

// Run is the executor session a direct task waits on.
type Run struct {
	ID     int32
	Status string
	// Report is the executor's finish_work payload, nil if it never finished.
	Report *Report
	Error  string
	// VaultLocked is set when the session ended because the model's API key
	// was sealed. That is not the task's failure and not an attempt.
	VaultLocked bool
	// ModelFailure is true when the session ended because its model could
	// not be called: the provider refused, was unreachable, or ran out.
	ModelFailure bool
}

// Run statuses the workflow distinguishes.
const (
	RunRunning   = "running"
	RunPaused    = "paused"
	RunResuming  = "resuming"
	RunCompleted = "completed"
	RunFailed    = "failed"
	RunCanceled  = "canceled"
)

// Report is what an executor says when it finishes.
type Report struct {
	Status        string   `json:"status"`
	Summary       string   `json:"summary"`
	Details       string   `json:"details,omitempty"`
	Evidence      []string `json:"evidence,omitempty"`
	OpenQuestions []string `json:"open_questions,omitempty"`
	Verdict       string   `json:"verdict,omitempty"`
}

// Report statuses.
const (
	ReportDone           = "done"
	ReportFailed         = "failed"
	ReportCannotComplete = "cannot_complete"
)

// ModelStatus says whether the model a task needs can be called right now.
type ModelStatus int

const (
	ModelReady ModelStatus = iota
	// ModelVaultLocked: the owner of the model's API key is signed out.
	ModelVaultLocked
	// ModelNotConfigured: no model is set for the task's tier.
	ModelNotConfigured
)

// Snapshot is everything Evaluate needs to know about a task at one moment.
type Snapshot struct {
	Task Task
	// Prerequisites are the tasks this one depends on.
	Prerequisites []Prerequisite
	Children      []Child
	// Run is the session named by the task's run, if any.
	Run *Run
	// HumanAnswer is the reply to the question the task waits on, if it came.
	HumanAnswer *string
	// HandledStepID is the newest step in which an adjust decision dealt with
	// failed subtasks; subtasks created before it have been dealt with.
	HandledStepID int64
	// SettledStepID is the newest step at which a person responded to the
	// task: answered it or ran it again. Subtasks created before it that could
	// not run have already been brought to them.
	SettledStepID int64
	// InspectsUsed counts read-only calls since the current phase began.
	InspectsUsed int
	// QuestionRounds counts the times questions were asked since the current
	// phase began or a person last responded, whichever is later.
	QuestionRounds int
	Model          ModelStatus
	// ModelDetail explains a model that is not ready, for the user.
	ModelDetail string
	// WorkspaceFree is true when no other task holds the tree's worktree.
	WorkspaceFree bool
	Roles         []string
	Now           time.Time
}

// Inc is a field value meaning "add this to the stored number".
type Inc int

// Step is a journal entry a transition appends.
type Step struct {
	Kind      string
	Phase     string
	ToolName  string
	ToolArgs  string
	Result    string
	Error     string
	RefTaskID *int32
	RunID     *int32
}

// TaskDraft is a subtask a transition creates.
type TaskDraft struct {
	// Key identifies the draft inside its transition; DependsOnKeys refers to
	// other drafts by it and DependsOnIDs to existing siblings.
	Key           string
	Title         string
	Instructions  string
	Type          string
	Mode          string
	Role          string
	ReviewRound   int
	DependsOnKeys []string
	DependsOnIDs  []int32
}

// Transition is one atomic change to a task: new field values, journal
// entries, decisions, subtasks, and the side effects that go with them.
// The driver persists all of it in a single transaction.
type Transition struct {
	// Fields are column values to set; an Inc value adds to the column.
	Fields    map[string]interface{}
	Steps     []Step
	Decisions []DecisionInput
	NewTasks  []TaskDraft
	// AskHuman, when set, posts this question to the human; the driver stores
	// the question's ID as what the task waits on.
	AskHuman string
	// StartRun creates the executor session the task then waits on.
	StartRun bool
	// ClaimWorkspace takes the tree's worktree; the transition fails if
	// another task got there first. ReleaseWorkspace gives it back.
	ClaimWorkspace   bool
	ReleaseWorkspace bool
	// PublishPR opens or updates the pull request once the change commits.
	PublishPR bool
	// CancelDescendants, when set, cancels every unfinished task beneath this
	// one with that reason, in the same transaction.
	CancelDescendants string
	// Redirects move the tasks that wait for one subtask over to another.
	Redirects []Redirect
}

// Redirect makes every task that depends on an existing subtask depend on a
// draft of the same transition instead. The drafts themselves are left as
// they are: the fix for a review goes on depending on that review.
type Redirect struct {
	FromID int32
	ToKey  string
}

func (t *Transition) set(column string, value interface{}) *Transition {
	if t.Fields == nil {
		t.Fields = map[string]interface{}{}
	}
	t.Fields[column] = value
	return t
}

func (t *Transition) step(step Step) *Transition {
	t.Steps = append(t.Steps, step)
	return t
}

// wait puts the task into a tracked wait.
func (t *Transition) wait(on, detail string) *Transition {
	return t.set("waiting_on", on).set("wait_detail", detail)
}

// ready clears whatever the task was waiting on.
func (t *Transition) ready() *Transition {
	return t.set("waiting_on", models.TaskWaitNone).set("wait_ref", nil).set("wait_until", nil).set("wait_detail", "")
}

// NextKind says what the driver should do for a task right now.
type NextKind int

const (
	// Idle: nothing to do. The task is finished, not started, or waiting on
	// something that has not happened yet.
	Idle NextKind = iota
	// Apply: persist the transition, then evaluate again.
	Apply
	// SmartStep: call the smart model for this phase, then apply its answer.
	SmartStep
)

// Next is the outcome of evaluating a task.
type Next struct {
	Kind       NextKind
	Transition *Transition
	// Phase, Role and Tools describe the smart step to run.
	Phase string
	Role  string
	Tools []ToolSpec
}

func idle() Next               { return Next{Kind: Idle} }
func apply(t *Transition) Next { return Next{Kind: Apply, Transition: t} }
func isTerminal(s string) bool { return models.IsTerminalTaskStatus(s) }
func unsuccessful(s string) bool {
	return s == models.TaskStatusFailed || s == models.TaskStatusCanceled
}

// ToolContext is the tool context of the snapshot's task.
func (s Snapshot) ToolContext(b Budgets) ToolContext {
	// Only failures the last re-plan has not already dealt with can be retried.
	var failed []int32
	for _, child := range s.Children {
		if !child.Question && child.OriginStepID >= s.HandledStepID && unsuccessful(child.Status) {
			failed = append(failed, child.ID)
		}
	}
	// What has been asked already, and whether asking again could help: a
	// question that got an answer, or a considered "cannot be answered", is
	// settled. One whose executor never ran or crashed may be put again.
	var asked []AskedQuestion
	for _, child := range s.Children {
		if !child.Question {
			continue
		}
		settled := child.Status == models.TaskStatusDone ||
			child.ResultReason == models.TaskResultReportedFailure || child.ResultReason == models.TaskResultCannotComplete
		asked = append(asked, AskedQuestion{TaskID: child.ID, Question: questionOf(child.Instructions),
			Settled: settled, Answered: child.Status == models.TaskStatusDone, Pending: !isTerminal(child.Status)})
	}
	return ToolContext{Budgets: b, InspectsUsed: s.InspectsUsed, QuestionRounds: s.QuestionRounds, Asked: asked,
		Roles: s.Roles, Depth: s.Task.Depth, FailedSubtasks: failed}
}

// questionContextMark separates a question from the context given with it in
// the instructions of the subtask that answers it.
const questionContextMark = "\n\nContext:\n"

// questionOf is the question a question subtask was created to answer.
func questionOf(instructions string) string {
	question, _, _ := strings.Cut(instructions, questionContextMark)
	return strings.TrimSpace(question)
}

// Evaluate decides what should happen to a task next. It is the whole state
// machine: given the same snapshot it always returns the same answer, and an
// answer of Idle is always safe to act on again later.
func Evaluate(s Snapshot, b Budgets) Next {
	task := s.Task
	switch task.Status {
	case models.TaskStatusTodo, models.TaskStatusDependsOnTask:
		return gate(s, b)
	case models.TaskStatusBlocked, models.TaskStatusInProgress:
		// Handled below. A root is blocked exactly while it waits on a human
		// or an operator; its waits resolve the same way as any other's.
	default:
		return idle()
	}

	switch task.WaitingOn {
	case models.TaskWaitHuman:
		if s.HumanAnswer == nil {
			return idle()
		}
		t := (&Transition{}).ready().set("status", models.TaskStatusInProgress).
			step(Step{Kind: models.StepHumanAnswer, Phase: task.Phase, Result: *s.HumanAnswer})
		return apply(t)
	case models.TaskWaitOperator:
		return idle()
	case models.TaskWaitBackoff:
		if task.WaitUntil != nil && s.Now.Before(*task.WaitUntil) {
			return idle()
		}
		return apply((&Transition{}).ready())
	case models.TaskWaitVault, models.TaskWaitConfig:
		if s.Model != ModelReady {
			return idle()
		}
		return apply((&Transition{}).ready().step(Step{Kind: models.StepResumed, Phase: task.Phase, Result: "model available"}))
	case models.TaskWaitWorkspace:
		if !s.WorkspaceFree {
			return idle()
		}
		return apply((&Transition{}).ready())
	case models.TaskWaitRun:
		return resolveRun(s, b)
	case models.TaskWaitSubtasks:
		return resolveSubtasks(s, b)
	}

	if task.Status == models.TaskStatusBlocked {
		// Blocked with nothing tracked to wait on is not a state the workflow
		// put the task in; leave it to whoever did.
		return idle()
	}
	if task.Mode == models.TaskModeDirect {
		return startRun(s, b)
	}
	if task.Phase == "" {
		// In progress with no phase: begin at the beginning.
		first := TemplateFor(task.Type).First()
		return apply((&Transition{}).set("phase", first).step(Step{Kind: models.StepPhaseEntered, Phase: first}))
	}
	if task.Phase == models.TaskPhaseExecute {
		// Execution is always a wait on subtasks; with none pending it resolves.
		return resolveSubtasks(s, b)
	}
	if s.Model != ModelReady {
		return apply(waitForModel(s))
	}
	if task.SmartStepsUsed >= b.MaxSmartSteps {
		return apply(escalate(s, fmt.Sprintf("it used all %d smart steps allowed without finishing", b.MaxSmartSteps), nil))
	}
	return Next{
		Kind:  SmartStep,
		Phase: task.Phase,
		Role:  TemplateFor(task.Type).RoleFor(task.Phase),
		Tools: ToolsFor(task.Phase, s.ToolContext(b)),
	}
}

// gate decides whether a queued task may start. A subtask builds on what it
// depends on, so that work must not only have finished but have been accepted:
// a review that asked for changes holds its dependents back until the review
// of the fix accepts it.
func gate(s Snapshot, b Budgets) Next {
	task := s.Task
	cancel := func(why string) Next {
		// The work this task builds on will never arrive.
		t := (&Transition{}).ready().
			set("status", models.TaskStatusCanceled).
			set("result_reason", models.TaskResultPrerequisiteFailed).
			set("phase", "").
			step(Step{Kind: models.StepFinished, Result: "canceled: " + why})
		return apply(t)
	}
	blocked := false
	for _, prerequisite := range s.Prerequisites {
		if unsuccessful(prerequisite.Status) {
			return cancel("a task this one depends on did not succeed")
		}
		if prerequisite.Status != models.TaskStatusDone {
			blocked = true
			continue
		}
		if task.IsRoot || !requestedChanges(prerequisite.Type, prerequisite.Status, prerequisite.Verdict) ||
			answersReview(task.Type, task.ReviewRound, prerequisite.ReviewRound) {
			continue
		}
		if !fixRoundFollows(task.ParentType, prerequisite.ReviewRound, b) {
			return cancel("a review this one depends on requested changes")
		}
		// The parent starts a fix round and makes this task wait for the
		// review of the fix instead.
		blocked = true
	}
	if blocked {
		if task.Status == models.TaskStatusDependsOnTask {
			return idle()
		}
		return apply((&Transition{}).set("status", models.TaskStatusDependsOnTask))
	}

	phase := models.TaskPhaseExecute
	if task.Mode == models.TaskModeManaged {
		phase = TemplateFor(task.Type).First()
	}
	t := (&Transition{}).ready().
		set("status", models.TaskStatusInProgress).
		set("phase", phase).
		set("smart_steps_used", 0).set("adjust_cycles_used", 0).set("attempts_used", 0).
		set("result_reason", "").set("result_summary", "").set("result_details", "").
		set("result_evidence", "").set("result_verdict", "").
		step(Step{Kind: models.StepWorkflowStarted, Phase: phase})
	return apply(t)
}

func waitForModel(s Snapshot) *Transition {
	on := models.TaskWaitConfig
	if s.Model == ModelVaultLocked {
		on = models.TaskWaitVault
	}
	return (&Transition{}).wait(on, s.ModelDetail).
		step(Step{Kind: models.StepWaiting, Phase: s.Task.Phase, Result: s.ModelDetail})
}

// escalate ends a task's own attempts. A subtask fails up to its parent, which
// then re-plans. A root has nobody above it but the human, so it asks: it
// moves to adjust with fresh budgets and waits for the answer.
func escalate(s Snapshot, reason string, decisions []DecisionInput) *Transition {
	task := s.Task
	t := &Transition{Decisions: decisions}
	if !task.IsRoot {
		return t.ready().
			set("status", models.TaskStatusFailed).
			set("result_reason", models.TaskResultBudgetExhausted).
			set("result_summary", reason).
			set("phase", "").
			step(Step{Kind: models.StepFinished, Phase: task.Phase, Result: "failed: " + reason})
	}
	t.AskHuman = "I could not complete this task: " + reason + ".\n\nTell me how to proceed and I will continue from here."
	return t.wait(models.TaskWaitHuman, "needs your direction: "+reason).
		set("status", models.TaskStatusBlocked).
		set("phase", models.TaskPhaseAdjust).
		set("smart_steps_used", 0).set("adjust_cycles_used", 0).set("attempts_used", 0).
		step(Step{Kind: models.StepHumanQuestion, Phase: task.Phase, Result: t.AskHuman})
}

// modelUnavailable stops a managed task whose work cannot run because a model
// cannot be called. No plan fixes that, so the smart model is not asked to
// find one: a subtask passes the problem up unchanged, and a root stops what
// is still running beneath it and tells the human, staying in the phase it
// was in so it carries on from there once they reply.
func modelUnavailable(s Snapshot, detail string) *Transition {
	task := s.Task
	t := &Transition{CancelDescendants: models.TaskResultStopped}
	if !task.IsRoot {
		return t.ready().
			set("status", models.TaskStatusFailed).
			set("result_reason", models.TaskResultModelError).
			set("result_summary", detail).
			set("phase", "").
			step(Step{Kind: models.StepFinished, Phase: task.Phase, Result: "failed: " + detail})
	}
	t.AskHuman = "I had to stop: " + detail + "\n\n" +
		"This is a problem with the model or its provider, not with the task, so I did not try to work around it. " +
		"Check the model under LLM Providers → Default Models, then reply here and I will continue from where I stopped."
	return t.wait(models.TaskWaitHuman, detail).
		set("status", models.TaskStatusBlocked).
		set("attempts_used", 0).
		step(Step{Kind: models.StepHumanQuestion, Phase: task.Phase, Result: t.AskHuman})
}

// startRun begins an executor session for a direct task.
func startRun(s Snapshot, b Budgets) Next {
	task := s.Task
	if task.AttemptsUsed >= b.MaxExecutorAttempts {
		t := (&Transition{}).ready().
			set("status", models.TaskStatusFailed).
			set("result_reason", models.TaskResultRunError).
			set("result_summary", fmt.Sprintf("the executor session failed %d times without reporting a result", task.AttemptsUsed)).
			set("phase", "").
			step(Step{Kind: models.StepFinished, Phase: task.Phase, Result: "failed: executor attempts exhausted"})
		return apply(t)
	}
	if s.Model != ModelReady {
		return apply(waitForModel(s))
	}
	writer := WritesWorkspace(task.Type)
	if writer && !s.WorkspaceFree {
		return apply((&Transition{}).wait(models.TaskWaitWorkspace, "another task is changing the shared worktree"))
	}
	t := (&Transition{StartRun: true, ClaimWorkspace: writer}).
		wait(models.TaskWaitRun, "").
		set("attempts_used", Inc(1)).
		step(Step{Kind: models.StepRunStarted, Phase: task.Phase})
	return apply(t)
}

// resolveRun applies the outcome of the executor session a direct task was
// waiting on.
func resolveRun(s Snapshot, b Budgets) Next {
	task := s.Task
	run := s.Run
	if run != nil {
		switch run.Status {
		case RunRunning, RunPaused, RunResuming:
			return idle()
		}
	}
	// Whatever happened, the session is over: the task no longer points at it
	// and no longer holds the worktree.
	done := (&Transition{ReleaseWorkspace: WritesWorkspace(task.Type)}).ready().set("run_id", nil)
	var runID *int32
	if run != nil {
		id := run.ID
		runID = &id
	}

	if run != nil && run.Status == RunCompleted && run.Report != nil {
		report := run.Report
		evidence, _ := json.Marshal(report.Evidence)
		done.set("result_summary", report.Summary).
			set("result_details", reportDetails(report)).
			set("result_evidence", string(evidence)).
			set("result_verdict", report.Verdict).
			set("phase", "")
		status, reason := models.TaskStatusDone, ""
		switch report.Status {
		case ReportFailed:
			status, reason = models.TaskStatusFailed, models.TaskResultReportedFailure
		case ReportCannotComplete:
			status, reason = models.TaskStatusFailed, models.TaskResultCannotComplete
		}
		done.set("status", status).set("result_reason", reason).
			step(Step{Kind: models.StepRunFinished, Phase: task.Phase, RunID: runID, Result: report.Status + ": " + report.Summary}).
			step(Step{Kind: models.StepFinished, Phase: task.Phase, Result: status})
		return apply(done)
	}

	if run != nil && run.Status == RunCanceled {
		done.set("status", models.TaskStatusCanceled).
			set("result_reason", models.TaskResultStopped).
			set("phase", "").
			step(Step{Kind: models.StepRunFinished, Phase: task.Phase, RunID: runID, Result: "canceled"}).
			step(Step{Kind: models.StepFinished, Phase: task.Phase, Result: models.TaskStatusCanceled})
		return apply(done)
	}

	if run != nil && run.VaultLocked {
		return apply(done.wait(models.TaskWaitVault, run.Error).
			set("attempts_used", Inc(-1)).
			step(Step{Kind: models.StepWaiting, Phase: task.Phase, RunID: runID, Result: run.Error}))
	}

	// The session ended without a report: it crashed, ran out of turns, or
	// its row is gone. Try again while attempts remain.
	problem := "the executor session ended without reporting a result"
	if run != nil && run.Error != "" {
		problem = run.Error
	}
	done.step(Step{Kind: models.StepRunFinished, Phase: task.Phase, RunID: runID, Error: problem})
	reason := models.TaskResultRunError
	if run != nil && run.ModelFailure {
		// The model, not the task, is what failed. Starting again at once
		// meets the same refusal; a pause lets a rate limit or an outage pass.
		reason = models.TaskResultModelError
		if task.AttemptsUsed < b.MaxExecutorAttempts {
			return apply(done.wait(models.TaskWaitBackoff, "retrying after a model error").
				set("wait_until", s.Now.Add(b.backoff(task.AttemptsUsed))))
		}
	}
	if task.AttemptsUsed < b.MaxExecutorAttempts {
		return apply(done)
	}
	done.set("status", models.TaskStatusFailed).
		set("result_reason", reason).
		set("result_summary", problem).
		set("phase", "").
		step(Step{Kind: models.StepFinished, Phase: task.Phase, Result: "failed: " + problem})
	return apply(done)
}

func reportDetails(report *Report) string {
	details := strings.TrimSpace(report.Details)
	if len(report.OpenQuestions) == 0 {
		return details
	}
	var b strings.Builder
	b.WriteString(details)
	if details != "" {
		b.WriteString("\n\n")
	}
	b.WriteString("Open questions:\n")
	for _, question := range report.OpenQuestions {
		b.WriteString("- " + question + "\n")
	}
	return strings.TrimSpace(b.String())
}

// resolveSubtasks decides what a managed task does about the subtasks it is
// waiting on. A review that asked for changes is answered at once, while other
// subtasks may still be running; everything else waits until all have finished.
func resolveSubtasks(s Snapshot, b Budgets) Next {
	task := s.Task
	pending := false
	for _, child := range s.Children {
		if !isTerminal(child.Status) {
			pending = true
		}
	}
	// A subtask that could not run because its model cannot be called says
	// the same of every other one. Stop here rather than let each find out.
	for _, child := range s.Children {
		if child.ResultReason == models.TaskResultModelError && child.OriginStepID > s.SettledStepID {
			return apply(modelUnavailable(s, child.ResultSummary))
		}
	}
	if task.Phase != models.TaskPhaseExecute {
		if pending {
			return idle()
		}
		// The subtasks were questions; what came back goes into the next prompt.
		return apply((&Transition{}).ready().
			step(Step{Kind: models.StepSubtaskFinished, Phase: task.Phase, Result: questionOutcome(s.Children)}))
	}

	// Only work that the last re-plan has not already dealt with counts.
	var work []Child
	byID := make(map[int32]Child, len(s.Children))
	for _, child := range s.Children {
		byID[child.ID] = child
		if !child.Question && child.OriginStepID >= s.HandledStepID {
			work = append(work, child)
		}
	}
	template := TemplateFor(task.Type)

	// A review that asked for changes and has no fix yet: the engine runs the
	// fix-and-review-again round itself, up to the limit. Other tasks may
	// depend on the same review; only its fix counts as an answer to it.
	addressed := map[int32]bool{}
	for _, child := range work {
		for _, dependency := range child.DependsOn {
			if review, ok := byID[dependency]; ok && answersReview(child.Type, child.ReviewRound, review.ReviewRound) {
				addressed[dependency] = true
			}
		}
	}
	var rejected, exhausted []Child
	for _, child := range work {
		if !requestedChanges(child.Type, child.Status, child.ResultVerdict) || addressed[child.ID] {
			continue
		}
		if fixRoundFollows(task.Type, child.ReviewRound, b) {
			rejected = append(rejected, child)
		} else {
			exhausted = append(exhausted, child)
		}
	}
	if len(rejected) > 0 {
		t := (&Transition{}).wait(models.TaskWaitSubtasks, "")
		for _, review := range rejected {
			round := review.ReviewRound + 1
			fixKey, reviewKey := fmt.Sprintf("fix-%d", review.ID), fmt.Sprintf("review-%d", review.ID)
			subject := reviewSubject(review.Title)
			t.NewTasks = append(t.NewTasks,
				TaskDraft{
					Key:   fixKey,
					Title: fmt.Sprintf("%s (round %d): %s", fixTitlePrefix, round, subject),
					Instructions: "A review of earlier work requested changes. Make them.\n\nReview findings:\n" + review.ResultSummary +
						"\n\nRead the review's full report for details before you start.",
					Type: models.TaskTypeCoding, Mode: models.TaskModeDirect, Role: template.ExecutorRole,
					ReviewRound: round, DependsOnIDs: []int32{review.ID},
				},
				TaskDraft{
					Key:   reviewKey,
					Title: reviewTitlePrefix + subject,
					Instructions: "Review the changes made in response to the earlier review findings below. Confirm each finding is resolved and nothing else regressed.\n\nEarlier findings:\n" +
						review.ResultSummary,
					Type: models.TaskTypeReview, Mode: models.TaskModeDirect, Role: template.ReviewerRole,
					ReviewRound: round, DependsOnKeys: []string{fixKey},
				})
			// Whatever else was waiting for this review to accept the work now
			// waits for the review of the fix.
			t.Redirects = append(t.Redirects, Redirect{FromID: review.ID, ToKey: reviewKey})
			id := review.ID
			t.step(Step{Kind: models.StepReviewRound, Phase: task.Phase, RefTaskID: &id,
				Result: fmt.Sprintf("review requested changes; starting fix round %d", round)})
		}
		return apply(t)
	}
	if pending {
		return idle()
	}

	failed := len(exhausted) > 0
	var problems []string
	for _, review := range exhausted {
		problems = append(problems, fmt.Sprintf("review %d still requests changes", review.ID))
	}
	for _, child := range work {
		if unsuccessful(child.Status) {
			failed = true
			problems = append(problems, fmt.Sprintf("task %d %s (%s)", child.ID, child.Status, child.ResultReason))
		}
	}
	sort.Strings(problems)
	if failed {
		summary := strings.Join(problems, "; ")
		if task.AdjustCyclesUsed >= b.MaxAdjustCycles {
			return apply(escalate(s, fmt.Sprintf("it re-planned %d times and work is still failing: %s", task.AdjustCyclesUsed, summary), nil))
		}
		return apply((&Transition{}).ready().
			set("phase", models.TaskPhaseAdjust).
			set("adjust_cycles_used", Inc(1)).
			step(Step{Kind: models.StepPhaseEntered, Phase: models.TaskPhaseAdjust, Result: summary}))
	}
	return apply((&Transition{}).ready().
		set("phase", models.TaskPhaseVerify).
		step(Step{Kind: models.StepPhaseEntered, Phase: models.TaskPhaseVerify, Result: "all subtasks succeeded"}))
}

// questionOutcome says what came of the questions asked last: how many were
// answered and how many were not. A question nobody could answer is not an
// answer.
func questionOutcome(children []Child) string {
	var last int64
	for _, child := range children {
		if child.Question && child.OriginStepID > last {
			last = child.OriginStepID
		}
	}
	asked, answered := 0, 0
	for _, child := range children {
		if !child.Question || child.OriginStepID != last {
			continue
		}
		asked++
		if child.Status == models.TaskStatusDone {
			answered++
		}
	}
	switch {
	case asked == 0:
		return "nothing was asked"
	case answered == asked && asked == 1:
		return "the question was answered"
	case answered == asked:
		return fmt.Sprintf("all %d questions were answered", asked)
	case answered == 0 && asked == 1:
		return "the question could not be answered"
	case answered == 0:
		return fmt.Sprintf("none of the %d questions could be answered", asked)
	}
	return fmt.Sprintf("%d of %d questions were answered; %d could not be", answered, asked, asked-answered)
}

const (
	reviewTitlePrefix = "Review: "
	fixTitlePrefix    = "Address review findings"
)

// reviewSubject is what a review or a fix is about: its title without the
// words the engine put in front, so that later rounds do not stack them.
func reviewSubject(title string) string {
	title = strings.TrimPrefix(title, reviewTitlePrefix)
	if rest, ok := strings.CutPrefix(title, fixTitlePrefix+" (round "); ok {
		if _, subject, found := strings.Cut(rest, "): "); found {
			return subject
		}
	}
	return title
}

// reviewOf is the review the engine adds after a piece of implementation.
func reviewOf(template Template, key, title, instructions string, round int) TaskDraft {
	return TaskDraft{
		Key:   "review:" + key,
		Title: reviewTitlePrefix + reviewSubject(title),
		Instructions: "Review the work of the task you depend on against its instructions and done-when below. Check the actual changes, run what can be run, and report a verdict with specific findings.\n\nInstructions given:\n" +
			instructions,
		Type: models.TaskTypeReview, Mode: models.TaskModeDirect, Role: template.ReviewerRole,
		ReviewRound: round, DependsOnKeys: []string{key},
	}
}

// SpecItem is one checkable line of a definition of done or test plan, in the
// shape the task's acceptance_criteria and test_cases columns store.
type SpecItem struct {
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

func specItems(lines []string) string {
	items := make([]SpecItem, 0, len(lines))
	for i, line := range lines {
		items = append(items, SpecItem{ID: i + 1, Text: line, Status: "pending"})
	}
	encoded, _ := json.Marshal(items)
	return string(encoded)
}

func judgedItems(criteria []Criterion) string {
	items := make([]SpecItem, 0, len(criteria))
	for i, criterion := range criteria {
		status := "failed"
		if criterion.Passed {
			status = "passed"
		}
		items = append(items, SpecItem{ID: i + 1, Text: criterion.Criterion, Status: status, Note: criterion.Note})
	}
	encoded, _ := json.Marshal(items)
	return string(encoded)
}

// ApplyAction turns a smart model's validated answer into the transition it
// causes. arguments is the raw tool call, kept in the journal.
func ApplyAction(s Snapshot, b Budgets, action Action, arguments string) *Transition {
	task := s.Task
	template := TemplateFor(task.Type)
	t := &Transition{Decisions: action.Recorded()}
	// The call itself is always journaled first; the driver adds the prompt
	// and the model's raw reply to this step. A call that worked also clears
	// the count of consecutive failures.
	t.step(Step{Kind: models.StepSmartCall, Phase: task.Phase, ToolName: action.Tool(), ToolArgs: arguments}).
		set("smart_steps_used", Inc(1)).
		set("attempts_used", 0)

	advance := func() {
		next, ok := template.After(task.Phase)
		if !ok {
			next = models.TaskPhaseVerify
		}
		t.set("phase", next).step(Step{Kind: models.StepPhaseEntered, Phase: next})
	}

	switch a := action.(type) {
	case GetDecisionTree, GetExecutionState:
		// Read-only: the driver writes the tool's output into the step.

	case AskQuestions:
		for i, question := range a.Questions {
			instructions := question.Question
			if question.Context != "" {
				instructions += questionContextMark + question.Context
			}
			t.NewTasks = append(t.NewTasks, TaskDraft{
				Key:          fmt.Sprintf("q%d", i+1),
				Title:        titleFrom(question.Question),
				Instructions: instructions,
				Type:         question.Kind,
				Mode:         models.TaskModeDirect,
			})
		}
		t.wait(models.TaskWaitSubtasks, "")

	case AskHuman:
		t.AskHuman = a.Question
		t.wait(models.TaskWaitHuman, "waiting for your answer").
			step(Step{Kind: models.StepHumanQuestion, Phase: task.Phase, Result: a.Question})
		if task.IsRoot {
			t.set("status", models.TaskStatusBlocked)
		}

	case FinishRefinement:
		t.set("refined_description", a.Spec).set("acceptance_criteria", specItems(a.DefinitionOfDone))
		advance()

	case FinishDesign:
		t.set("design", a.Design)
		advance()

	case FinishTestPlan:
		t.set("test_cases", specItems(a.TestScenarios))
		advance()

	case CreateTasks:
		// Every piece of implementation is reviewed before the plan moves on;
		// a managed subtask verifies itself instead.
		reviewed := map[string]bool{}
		for _, planned := range a.Tasks {
			if template.ReviewImplementation && planned.Type == models.TaskTypeCoding && !planned.Managed {
				reviewed[planned.Key] = true
			}
		}
		for _, planned := range a.Tasks {
			mode := models.TaskModeDirect
			if planned.Managed {
				mode = models.TaskModeManaged
			}
			role := planned.Role
			if role == "" {
				role = template.ExecutorRole
				if planned.Type == models.TaskTypeReview {
					role = template.ReviewerRole
				}
			}
			// Work that builds on reviewed implementation waits for the review
			// to accept it, not only for the implementation to be written.
			dependsOn := append([]string(nil), planned.DependsOn...)
			for _, key := range planned.DependsOn {
				if reviewed[key] {
					dependsOn = append(dependsOn, "review:"+key)
				}
			}
			instructions := planned.Instructions + "\n\nDone when:\n" + planned.DoneWhen
			t.NewTasks = append(t.NewTasks, TaskDraft{
				Key:           planned.Key,
				Title:         planned.Title,
				Instructions:  instructions,
				Type:          planned.Type,
				Mode:          mode,
				Role:          role,
				DependsOnKeys: dependsOn,
			})
			if reviewed[planned.Key] {
				t.NewTasks = append(t.NewTasks, reviewOf(template, planned.Key, planned.Title, instructions, 0))
			}
		}
		t.set("phase", models.TaskPhaseExecute).wait(models.TaskWaitSubtasks, "").
			step(Step{Kind: models.StepPhaseEntered, Phase: models.TaskPhaseExecute})

	case RetryTasks:
		byID := make(map[int32]Child, len(s.Children))
		for _, child := range s.Children {
			byID[child.ID] = child
		}
		retried := make(map[int32]bool, len(a.TaskIDs))
		for _, id := range a.TaskIDs {
			retried[id] = true
		}
		// A new attempt at implementation is new code: it is reviewed like the
		// first one was. The review of the earlier attempt does not cover it,
		// so that review is replaced rather than run again beside the new one.
		reviewed := func(child Child) bool {
			return template.ReviewImplementation && child.Type == models.TaskTypeCoding
		}
		replaced := func(child Child) bool {
			if child.Type != models.TaskTypeReview {
				return false
			}
			for _, dependency := range child.DependsOn {
				if retried[dependency] && reviewed(byID[dependency]) {
					return true
				}
			}
			return false
		}
		key := func(id int32) string { return fmt.Sprintf("retry-%d", id) }
		for _, id := range a.TaskIDs {
			original := byID[id]
			if replaced(original) {
				continue
			}
			// Tasks retried together keep their order: what waited for an
			// earlier attempt waits for the new one, and for its review.
			var dependsOn []string
			for _, dependency := range original.DependsOn {
				if !retried[dependency] || replaced(byID[dependency]) {
					continue
				}
				dependsOn = append(dependsOn, key(dependency))
				if reviewed(byID[dependency]) {
					dependsOn = append(dependsOn, "review:"+key(dependency))
				}
			}
			instructions := original.Instructions + "\n\nThis is a new attempt at a task that did not succeed before. Do this differently:\n" + a.Instructions
			t.NewTasks = append(t.NewTasks, TaskDraft{
				Key:           key(id),
				Title:         original.Title,
				Instructions:  instructions,
				Type:          original.Type,
				Mode:          models.TaskModeDirect,
				Role:          original.Role,
				ReviewRound:   original.ReviewRound,
				DependsOnKeys: dependsOn,
			})
			if reviewed(original) {
				t.NewTasks = append(t.NewTasks, reviewOf(template, key(id), original.Title, instructions, original.ReviewRound))
			}
		}
		t.set("phase", models.TaskPhaseExecute).wait(models.TaskWaitSubtasks, "").
			step(Step{Kind: models.StepPhaseEntered, Phase: models.TaskPhaseExecute})

	case FinishAdjustment:
		t.set("phase", models.TaskPhaseVerify).
			step(Step{Kind: models.StepPhaseEntered, Phase: models.TaskPhaseVerify, Result: a.Reason})

	case Escalate:
		escalation := escalate(s, a.Reason, nil)
		for column, value := range escalation.Fields {
			t.set(column, value)
		}
		t.Steps = append(t.Steps, escalation.Steps...)
		t.AskHuman = escalation.AskHuman

	case FinishVerification:
		t.set("acceptance_criteria", judgedItems(a.Criteria))
		if !a.Passed {
			if task.AdjustCyclesUsed >= b.MaxAdjustCycles {
				escalation := escalate(s, fmt.Sprintf("verification still fails after %d re-plans: %s", task.AdjustCyclesUsed, a.Summary), nil)
				for column, value := range escalation.Fields {
					t.set(column, value)
				}
				t.Steps = append(t.Steps, escalation.Steps...)
				t.AskHuman = escalation.AskHuman
				break
			}
			t.set("phase", models.TaskPhaseAdjust).set("adjust_cycles_used", Inc(1)).
				step(Step{Kind: models.StepPhaseEntered, Phase: models.TaskPhaseAdjust, Result: "verification failed: " + a.Summary})
			break
		}
		// A root stops for the human's review; a subtask is simply done.
		status := models.TaskStatusDone
		if task.IsRoot {
			status = models.TaskStatusInReview
			t.PublishPR = task.Type == models.TaskTypeCoding
		}
		t.ready().set("status", status).set("phase", "").
			set("result_summary", a.Summary).set("result_reason", "").
			step(Step{Kind: models.StepFinished, Phase: task.Phase, Result: status + ": " + a.Summary})
	}
	return t
}

// FailureKind classifies why a smart call produced no usable answer.
type FailureKind int

const (
	// FailureTransient: the provider failed or the model never produced an
	// acceptable tool call. Worth retrying after a wait.
	FailureTransient FailureKind = iota
	// FailureVaultLocked and FailureNotConfigured are not the task's fault
	// and are not counted against it: it waits until the cause is fixed.
	FailureVaultLocked
	FailureNotConfigured
	// FailureModel: the model could not be called at all. Retried like a
	// transient failure, but when the retries run out no re-plan can help.
	FailureModel
)

// ApplyFailure is the transition for a smart call that produced no answer.
func ApplyFailure(s Snapshot, b Budgets, kind FailureKind, message string) *Transition {
	task := s.Task
	switch kind {
	case FailureVaultLocked:
		return (&Transition{}).wait(models.TaskWaitVault, message).
			step(Step{Kind: models.StepWaiting, Phase: task.Phase, Result: message})
	case FailureNotConfigured:
		return (&Transition{}).wait(models.TaskWaitConfig, message).
			step(Step{Kind: models.StepWaiting, Phase: task.Phase, Result: message})
	}
	failures := task.AttemptsUsed + 1
	if failures >= b.MaxSmartFailures {
		var t *Transition
		if kind == FailureModel {
			t = modelUnavailable(s, fmt.Sprintf("the model that decides what to do could not be called %d times in a row: %s", failures, message))
		} else {
			t = escalate(s, fmt.Sprintf("the model failed %d times in a row: %s", failures, message), nil)
		}
		t.Steps = append([]Step{{Kind: models.StepSmartError, Phase: task.Phase, Error: message}}, t.Steps...)
		return t
	}
	until := s.Now.Add(b.backoff(failures))
	return (&Transition{}).wait(models.TaskWaitBackoff, "retrying after a model error").
		set("wait_until", until).
		set("attempts_used", Inc(1)).
		step(Step{Kind: models.StepSmartError, Phase: task.Phase, Error: message})
}

// Stop is the transition for a user stopping a task. A root parks, visibly,
// until the user runs it again; a subtask is canceled.
func Stop(s Snapshot) *Transition {
	task := s.Task
	t := (&Transition{ReleaseWorkspace: true, CancelDescendants: models.TaskResultStopped}).ready().set("run_id", nil)
	if task.IsRoot {
		return t.wait(models.TaskWaitOperator, "stopped by you").
			set("status", models.TaskStatusBlocked).
			step(Step{Kind: models.StepStopped, Phase: task.Phase})
	}
	return t.set("status", models.TaskStatusCanceled).
		set("result_reason", models.TaskResultStopped).
		set("phase", "").
		step(Step{Kind: models.StepStopped, Phase: task.Phase})
}

// Queue is the transition for a user starting a task that has never run. It
// only queues the task; whether it must wait for other tasks is decided when
// it is picked up.
func Queue() *Transition {
	return (&Transition{}).set("status", models.TaskStatusTodo)
}

// Place is the transition for a user putting a task that is at rest where
// they want it: back in the backlog, into review, or done. Whatever the
// workflow was tracking for it is cleared, so nothing is left waiting.
func Place(s Snapshot, status string) *Transition {
	return (&Transition{ReleaseWorkspace: true, CancelDescendants: models.TaskResultStopped}).ready().
		set("run_id", nil).
		set("status", status).
		set("phase", "").
		step(Step{Kind: models.StepNote, Phase: s.Task.Phase, Result: "moved to " + status + " by a human"})
}

// Rerun is the transition for a user running a finished or parked root task
// again. A task that already has a specification re-plans from where it is;
// one that never got that far starts over. direction is what the user said
// when asking for it, if anything; the next step sees it as why it is there.
func Rerun(s Snapshot, hasSpec bool, direction string) *Transition {
	task := s.Task
	phase := models.TaskPhaseExecute
	if task.Mode == models.TaskModeManaged {
		phase = TemplateFor(task.Type).First()
		if hasSpec {
			phase = models.TaskPhaseAdjust
		}
	}
	return (&Transition{}).ready().
		set("status", models.TaskStatusInProgress).
		set("phase", phase).
		set("smart_steps_used", 0).set("adjust_cycles_used", 0).set("attempts_used", 0).
		set("result_reason", "").
		step(Step{Kind: models.StepRerun, Phase: phase, Result: direction})
}

func titleFrom(text string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	const limit = 120
	if runes := []rune(line); len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return line
}
