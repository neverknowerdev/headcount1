package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"agent-orchestrator/db/models"
)

// Names of the tools a smart model answers with. Each smart step ends in
// exactly one of them.
const (
	ToolAskQuestions       = "ask_questions"
	ToolAskHuman           = "ask_human"
	ToolFinishRefinement   = "finish_refinement"
	ToolFinishDesign       = "finish_design"
	ToolFinishTestPlan     = "finish_test_plan"
	ToolCreateTasks        = "create_tasks"
	ToolRetryTasks         = "retry_tasks"
	ToolFinishAdjustment   = "finish_adjustment"
	ToolEscalate           = "escalate"
	ToolFinishVerification = "finish_verification"
	ToolGetDecisionTree    = "get_decision_tree"
	ToolGetExecutionState  = "get_execution_state"
)

// ToolSpec describes one tool to the model.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolContext is what the tool set and its validation depend on besides the
// phase.
type ToolContext struct {
	Budgets Budgets
	// InspectsUsed counts the read-only calls already made in this phase.
	InspectsUsed int
	// Roles are the role names subtasks may be assigned to. Empty means any.
	Roles []string
	// Depth is the task's depth in its tree; it decides whether a subtask may
	// be managed.
	Depth int
	// FailedSubtasks are the IDs retry_tasks may name.
	FailedSubtasks []int32
	// QuestionRounds counts the times questions were asked in this phase.
	QuestionRounds int
	// Asked are the questions this task has already put to its subtasks.
	Asked []AskedQuestion
}

// AskedQuestion is a question already put, and what became of it.
type AskedQuestion struct {
	TaskID   int32
	Question string
	// Settled: it was answered, or an executor looked and reported that it
	// cannot be. Asking it again would get the same result.
	Settled  bool
	Answered bool
	// Pending: the subtask answering it has not finished.
	Pending bool
}

func (c ToolContext) questionRoundsLeft() bool {
	return c.QuestionRounds < c.Budgets.MaxQuestionRoundsPerPhase
}

func (c ToolContext) inspectsLeft() bool {
	return c.InspectsUsed < c.Budgets.MaxInspectsPerPhase
}

func (c ToolContext) managedSubtasksAllowed() bool {
	return c.Depth+1 <= c.Budgets.MaxManagedDepth
}

// IsInspectTool reports whether a tool only reads: it changes nothing and its
// output is handed to the model's next step.
func IsInspectTool(name string) bool {
	return name == ToolGetDecisionTree || name == ToolGetExecutionState
}

// ToolsFor is the tool set of a phase. Read-only tools drop out once their
// budget for the phase is spent, which forces the model to act.
func ToolsFor(phase string, c ToolContext) []ToolSpec {
	var names []string
	switch phase {
	case models.TaskPhaseRefine:
		names = []string{ToolAskQuestions, ToolAskHuman, ToolFinishRefinement}
	case models.TaskPhaseDesign:
		names = []string{ToolAskQuestions, ToolAskHuman, ToolFinishDesign}
	case models.TaskPhaseTestPlan:
		names = []string{ToolAskQuestions, ToolAskHuman, ToolFinishTestPlan}
	case models.TaskPhasePlan:
		names = []string{ToolAskQuestions, ToolAskHuman, ToolCreateTasks}
	case models.TaskPhaseAdjust:
		names = []string{ToolGetDecisionTree, ToolGetExecutionState, ToolAskQuestions, ToolAskHuman,
			ToolCreateTasks, ToolRetryTasks, ToolFinishAdjustment, ToolEscalate}
	case models.TaskPhaseVerify:
		names = []string{ToolGetExecutionState, ToolAskQuestions, ToolFinishVerification}
	}
	specs := make([]ToolSpec, 0, len(names))
	for _, name := range names {
		if IsInspectTool(name) && !c.inspectsLeft() {
			continue
		}
		if name == ToolRetryTasks && len(c.FailedSubtasks) == 0 {
			continue
		}
		if name == ToolAskQuestions && !c.questionRoundsLeft() {
			// Enough rounds of questions: decide with what is known, or ask
			// the human.
			continue
		}
		specs = append(specs, toolSpec(name, c))
	}
	return specs
}

const decisionsSchema = `{
  "type": "array",
  "description": "What you decided in this step and why. Record every real choice: the approach taken, what was ruled out, and anything you are assuming without proof.",
  "items": {
    "type": "object",
    "properties": {
      "title": {"type": "string", "description": "The decision in a few words."},
      "decision": {"type": "string", "description": "What was decided."},
      "rationale": {"type": "string", "description": "Why."},
      "alternatives": {"type": "array", "items": {"type": "string"}, "description": "Options considered and rejected."},
      "assumption": {"type": "boolean", "description": "True when this is assumed rather than established."},
      "parent_id": {"type": "integer", "description": "ID of an earlier decision this one refines, if any."}
    },
    "required": ["title", "decision", "rationale"]
  }%s
}`

func decisions(required bool) string {
	if required {
		return fmt.Sprintf(decisionsSchema, `,
  "minItems": 1`)
	}
	return fmt.Sprintf(decisionsSchema, "")
}

func object(properties map[string]string, required ...string) json.RawMessage {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	for i, key := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%q:%s", key, properties[key])
	}
	b.WriteString(`},"required":`)
	if required == nil {
		required = []string{}
	}
	requiredJSON, _ := json.Marshal(required)
	b.Write(requiredJSON)
	b.WriteString(`,"additionalProperties":false}`)
	return json.RawMessage(b.String())
}

func stringList(description string) string {
	encoded, _ := json.Marshal(description)
	return fmt.Sprintf(`{"type":"array","items":{"type":"string"},"minItems":1,"description":%s}`, encoded)
}

func text(description string) string {
	encoded, _ := json.Marshal(description)
	return fmt.Sprintf(`{"type":"string","description":%s}`, encoded)
}

func enum(description string, values ...string) string {
	encodedDescription, _ := json.Marshal(description)
	encodedValues, _ := json.Marshal(values)
	return fmt.Sprintf(`{"type":"string","enum":%s,"description":%s}`, encodedValues, encodedDescription)
}

func toolSpec(name string, c ToolContext) ToolSpec {
	switch name {
	case ToolAskQuestions:
		return ToolSpec{
			Name: name,
			Description: "Ask questions you need answered before you can decide. Each question is researched independently and in parallel by an agent that can read the repository, run commands and search the web; the answers come back to you. " +
				"Ask precise, self-contained questions: the researcher knows nothing but the question and its context. If a question cannot be answered, you are told why and what was tried.",
			Parameters: object(map[string]string{
				"questions": fmt.Sprintf(`{"type":"array","minItems":1,"maxItems":%d,"items":{"type":"object","properties":{
"question":%s,
"context":%s,
"kind":%s},"required":["question"]}}`, c.Budgets.MaxQuestionsPerStep,
					text("The question, complete on its own."),
					text("What the researcher needs to know to answer it: where to look, what is already known."),
					enum("research finds an answer; review judges something against criteria and returns a verdict.", models.TaskTypeResearch, models.TaskTypeReview)),
				"decisions": decisions(false),
			}, "questions", "decisions"),
		}
	case ToolAskHuman:
		return ToolSpec{
			Name: name,
			Description: "Ask the human who owns this task. Use it only for what research cannot settle and what matters to the outcome: intent, priorities, preferences, approval of a risky choice. The task waits until they answer. " +
				"Prefer asking researchers first; prefer recording a reasonable assumption over asking about details.",
			Parameters: object(map[string]string{
				"question":  text("The question, written for the human, with the options you see if any."),
				"why":       text("Why this needs the human and what you will do with the answer."),
				"decisions": decisions(false),
			}, "question", "why", "decisions"),
		}
	case ToolFinishRefinement:
		return ToolSpec{
			Name:        name,
			Description: "Finish refinement once you understand the task fully. Writes the specification everyone downstream works from.",
			Parameters: object(map[string]string{
				"spec":               text("What must be done and why, with scope, constraints and references, complete enough that nobody has to ask again."),
				"definition_of_done": stringList("Checkable criteria, each one a single statement that is either met or not."),
				"decisions":          decisions(true),
			}, "spec", "definition_of_done", "decisions"),
		}
	case ToolFinishDesign:
		return ToolSpec{
			Name:        name,
			Description: "Finish the technical design: how the specification will be implemented.",
			Parameters: object(map[string]string{
				"design":    text("The technical design and specification: components, interfaces, data, the order of work, risks."),
				"decisions": decisions(true),
			}, "design", "decisions"),
		}
	case ToolFinishTestPlan:
		return ToolSpec{
			Name:        name,
			Description: "Finish the test plan: the scenarios that will show the work is correct.",
			Parameters: object(map[string]string{
				"test_scenarios": stringList("Test scenarios, each with what is done and what must be observed."),
				"decisions":      decisions(true),
			}, "test_scenarios", "decisions"),
		}
	case ToolCreateTasks:
		managed := ""
		if c.managedSubtasksAllowed() {
			managed = `,"managed":{"type":"boolean","description":"True for a piece large or uncertain enough to need its own refinement, plan and verification. Default false: an executor does it in one session."}`
		}
		role := text("Role that does the task. Leave empty for the default.")
		if len(c.Roles) > 0 {
			role = enum("Role that does the task. Leave out for the default.", c.Roles...)
		}
		return ToolSpec{
			Name: name,
			Description: "Delegate the work as small tasks. Tasks with no dependency run in parallel; a task that names others in depends_on starts only after they succeed. " +
				"Keep each task small enough for one focused session, and give it everything it needs: the executor sees only its own instructions.",
			Parameters: object(map[string]string{
				"tasks": fmt.Sprintf(`{"type":"array","minItems":1,"maxItems":%d,"items":{"type":"object","properties":{
"key":%s,
"title":%s,
"instructions":%s,
"done_when":%s,
"type":%s,
"role":%s,
"depends_on":{"type":"array","items":{"type":"string"},"description":"Keys of tasks in this list that must succeed first."}%s},
"required":["key","title","instructions","done_when","type"]}}`, c.Budgets.MaxTasksPerStep,
					text("A short name for this task, unique in this list, used in depends_on."),
					text("What the task is, in a line."),
					text("Exactly what to do, with the context, files and constraints needed. Complete on its own."),
					text("How the executor and a reviewer know it is finished."),
					enum("coding changes the repository; research finds and reports; review judges a result; general produces something else.",
						models.TaskTypeCoding, models.TaskTypeResearch, models.TaskTypeReview, models.TaskTypeGeneral),
					role, managed),
				"decisions": decisions(true),
			}, "tasks", "decisions"),
		}
	case ToolRetryTasks:
		return ToolSpec{
			Name:        name,
			Description: "Run failed or canceled tasks again as new attempts, with revised instructions. Use it when the task was right but its instructions or approach were not. Tasks retried together keep the order they had, so name a failed task and the tasks that were canceled because of it in one call.",
			Parameters: object(map[string]string{
				"task_ids":     `{"type":"array","items":{"type":"integer"},"minItems":1,"description":"IDs of the failed tasks to attempt again."}`,
				"instructions": text("What to do differently this time. Added to each task's original instructions."),
				"decisions":    decisions(true),
			}, "task_ids", "instructions", "decisions"),
		}
	case ToolFinishAdjustment:
		return ToolSpec{
			Name:        name,
			Description: "Decide that no further work is needed and go to verification with the results as they stand.",
			Parameters: object(map[string]string{
				"reason":    text("Why the results are sufficient despite what failed."),
				"decisions": decisions(true),
			}, "reason", "decisions"),
		}
	case ToolEscalate:
		return ToolSpec{
			Name:        name,
			Description: "Give up on this task as it stands and hand it back with an explanation. Use it when no plan you can make will meet the definition of done.",
			Parameters: object(map[string]string{
				"reason":    text("What was tried, why it cannot succeed, and what would be needed."),
				"decisions": decisions(true),
			}, "reason", "decisions"),
		}
	case ToolFinishVerification:
		return ToolSpec{
			Name:        name,
			Description: "Give the verdict: do the results meet the definition of done? Judge every criterion on the evidence in front of you, not on what was reported as done.",
			Parameters: object(map[string]string{
				"passed": `{"type":"boolean","description":"True only if every criterion is met."}`,
				"criteria": `{"type":"array","minItems":1,"items":{"type":"object","properties":{
"criterion":{"type":"string"},
"passed":{"type":"boolean"},
"note":{"type":"string","description":"The evidence, or what is missing."}},
"required":["criterion","passed","note"]}}`,
				"summary":   text("What was delivered, and for a failure what must change."),
				"decisions": decisions(true),
			}, "passed", "criteria", "summary", "decisions"),
		}
	case ToolGetDecisionTree:
		return ToolSpec{
			Name:        name,
			Description: "Read the decisions recorded so far, as a tree following the task hierarchy. Changes nothing; you get the result in your next step.",
			Parameters: object(map[string]string{
				"scope": enum("task: this task and its subtasks. root: the whole tree this task belongs to.", "task", "root"),
			}, "scope"),
		}
	case ToolGetExecutionState:
		return ToolSpec{
			Name:        name,
			Description: "Read the full reports of subtasks: details, evidence, checkpoints and open questions. Changes nothing; you get the result in your next step.",
			Parameters: object(map[string]string{
				"task_ids": `{"type":"array","items":{"type":"integer"},"description":"Subtasks to read in full. Leave empty for all of them."}`,
			}),
		}
	}
	return ToolSpec{Name: name}
}

// DecisionInput is a decision as a model states it.
type DecisionInput struct {
	Title        string   `json:"title"`
	Decision     string   `json:"decision"`
	Rationale    string   `json:"rationale"`
	Alternatives []string `json:"alternatives,omitempty"`
	Assumption   bool     `json:"assumption,omitempty"`
	ParentID     int64    `json:"parent_id,omitempty"`
}

// Kind is the stored kind of the decision.
func (d DecisionInput) Kind() string {
	if d.Assumption {
		return models.DecisionKindAssumption
	}
	return models.DecisionKindDecision
}

// Question is one item of ask_questions.
type Question struct {
	Question string `json:"question"`
	Context  string `json:"context,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// PlannedTask is one item of create_tasks.
type PlannedTask struct {
	Key          string   `json:"key"`
	Title        string   `json:"title"`
	Instructions string   `json:"instructions"`
	DoneWhen     string   `json:"done_when"`
	Type         string   `json:"type"`
	Role         string   `json:"role,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	Managed      bool     `json:"managed,omitempty"`
}

// Criterion is one judged item of finish_verification.
type Criterion struct {
	Criterion string `json:"criterion"`
	Passed    bool   `json:"passed"`
	Note      string `json:"note"`
}

// Action is a validated answer from a smart model: one tool call, parsed.
type Action interface {
	// Tool is the name of the tool the model called.
	Tool() string
	// Recorded are the decisions the answer carries.
	Recorded() []DecisionInput
}

type AskQuestions struct {
	Questions []Question      `json:"questions"`
	Decisions []DecisionInput `json:"decisions"`
}

type AskHuman struct {
	Question  string          `json:"question"`
	Why       string          `json:"why"`
	Decisions []DecisionInput `json:"decisions"`
}

type FinishRefinement struct {
	Spec             string          `json:"spec"`
	DefinitionOfDone []string        `json:"definition_of_done"`
	Decisions        []DecisionInput `json:"decisions"`
}

type FinishDesign struct {
	Design    string          `json:"design"`
	Decisions []DecisionInput `json:"decisions"`
}

type FinishTestPlan struct {
	TestScenarios []string        `json:"test_scenarios"`
	Decisions     []DecisionInput `json:"decisions"`
}

type CreateTasks struct {
	Tasks     []PlannedTask   `json:"tasks"`
	Decisions []DecisionInput `json:"decisions"`
}

type RetryTasks struct {
	TaskIDs      []int32         `json:"task_ids"`
	Instructions string          `json:"instructions"`
	Decisions    []DecisionInput `json:"decisions"`
}

type FinishAdjustment struct {
	Reason    string          `json:"reason"`
	Decisions []DecisionInput `json:"decisions"`
}

type Escalate struct {
	Reason    string          `json:"reason"`
	Decisions []DecisionInput `json:"decisions"`
}

type FinishVerification struct {
	Passed    bool            `json:"passed"`
	Criteria  []Criterion     `json:"criteria"`
	Summary   string          `json:"summary"`
	Decisions []DecisionInput `json:"decisions"`
}

type GetDecisionTree struct {
	Scope string `json:"scope"`
}

type GetExecutionState struct {
	TaskIDs []int32 `json:"task_ids"`
}

func (AskQuestions) Tool() string       { return ToolAskQuestions }
func (AskHuman) Tool() string           { return ToolAskHuman }
func (FinishRefinement) Tool() string   { return ToolFinishRefinement }
func (FinishDesign) Tool() string       { return ToolFinishDesign }
func (FinishTestPlan) Tool() string     { return ToolFinishTestPlan }
func (CreateTasks) Tool() string        { return ToolCreateTasks }
func (RetryTasks) Tool() string         { return ToolRetryTasks }
func (FinishAdjustment) Tool() string   { return ToolFinishAdjustment }
func (Escalate) Tool() string           { return ToolEscalate }
func (FinishVerification) Tool() string { return ToolFinishVerification }
func (GetDecisionTree) Tool() string    { return ToolGetDecisionTree }
func (GetExecutionState) Tool() string  { return ToolGetExecutionState }

func (a AskQuestions) Recorded() []DecisionInput       { return a.Decisions }
func (a AskHuman) Recorded() []DecisionInput           { return a.Decisions }
func (a FinishRefinement) Recorded() []DecisionInput   { return a.Decisions }
func (a FinishDesign) Recorded() []DecisionInput       { return a.Decisions }
func (a FinishTestPlan) Recorded() []DecisionInput     { return a.Decisions }
func (a CreateTasks) Recorded() []DecisionInput        { return a.Decisions }
func (a RetryTasks) Recorded() []DecisionInput         { return a.Decisions }
func (a FinishAdjustment) Recorded() []DecisionInput   { return a.Decisions }
func (a Escalate) Recorded() []DecisionInput           { return a.Decisions }
func (a FinishVerification) Recorded() []DecisionInput { return a.Decisions }
func (GetDecisionTree) Recorded() []DecisionInput      { return nil }
func (GetExecutionState) Recorded() []DecisionInput    { return nil }

// ParseAction turns a model's tool call into a validated Action. The error it
// returns is written for the model: it is sent back as the reason the answer
// was not accepted, so it says what to fix.
func ParseAction(phase, tool string, arguments json.RawMessage, c ToolContext) (Action, error) {
	allowed := false
	for _, spec := range ToolsFor(phase, c) {
		if spec.Name == tool {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("%s is not available in the %s phase", tool, phase)
	}
	if len(strings.TrimSpace(string(arguments))) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	decode := func(target interface{}) error {
		if err := json.Unmarshal(arguments, target); err != nil {
			return fmt.Errorf("arguments are not valid JSON for %s: %v", tool, err)
		}
		return nil
	}

	switch tool {
	case ToolAskQuestions:
		var a AskQuestions
		if err := decode(&a); err != nil {
			return nil, err
		}
		if len(a.Questions) == 0 {
			return nil, errors.New("questions must contain at least one question")
		}
		if len(a.Questions) > c.Budgets.MaxQuestionsPerStep {
			return nil, fmt.Errorf("at most %d questions per step; ask the most important ones first", c.Budgets.MaxQuestionsPerStep)
		}
		for i := range a.Questions {
			a.Questions[i].Question = strings.TrimSpace(a.Questions[i].Question)
			if a.Questions[i].Question == "" {
				return nil, fmt.Errorf("questions[%d].question is empty", i)
			}
			switch a.Questions[i].Kind {
			case "":
				a.Questions[i].Kind = models.TaskTypeResearch
			case models.TaskTypeResearch, models.TaskTypeReview:
			default:
				return nil, fmt.Errorf("questions[%d].kind must be research or review", i)
			}
			for j := 0; j < i; j++ {
				if sameQuestion(a.Questions[i].Question, a.Questions[j].Question) {
					return nil, fmt.Errorf("questions[%d] repeats questions[%d]; ask each thing once", i, j)
				}
			}
			for _, asked := range c.Asked {
				if !asked.Settled && !asked.Pending || !sameQuestion(a.Questions[i].Question, asked.Question) {
					continue
				}
				outcome := "an executor looked into it and reported that it cannot be answered with what is available"
				switch {
				case asked.Pending:
					outcome = "it is still being answered"
				case asked.Answered:
					outcome = "it was answered"
				}
				return nil, fmt.Errorf("questions[%d] was already asked (task %d) and %s. Do not ask it again: "+
					"use that result, ask something different, or put it to the human", i, asked.TaskID, outcome)
			}
		}
		return a, validateDecisions(a.Decisions, false)
	case ToolAskHuman:
		var a AskHuman
		if err := decode(&a); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Question) == "" {
			return nil, errors.New("question is empty")
		}
		if strings.TrimSpace(a.Why) == "" {
			return nil, errors.New("why is empty: say why this needs the human")
		}
		return a, validateDecisions(a.Decisions, false)
	case ToolFinishRefinement:
		var a FinishRefinement
		if err := decode(&a); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Spec) == "" {
			return nil, errors.New("spec is empty")
		}
		var err error
		if a.DefinitionOfDone, err = cleanList("definition_of_done", a.DefinitionOfDone); err != nil {
			return nil, err
		}
		return a, validateDecisions(a.Decisions, true)
	case ToolFinishDesign:
		var a FinishDesign
		if err := decode(&a); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Design) == "" {
			return nil, errors.New("design is empty")
		}
		return a, validateDecisions(a.Decisions, true)
	case ToolFinishTestPlan:
		var a FinishTestPlan
		if err := decode(&a); err != nil {
			return nil, err
		}
		var err error
		if a.TestScenarios, err = cleanList("test_scenarios", a.TestScenarios); err != nil {
			return nil, err
		}
		return a, validateDecisions(a.Decisions, true)
	case ToolCreateTasks:
		var a CreateTasks
		if err := decode(&a); err != nil {
			return nil, err
		}
		if err := validatePlannedTasks(a.Tasks, c); err != nil {
			return nil, err
		}
		return a, validateDecisions(a.Decisions, true)
	case ToolRetryTasks:
		var a RetryTasks
		if err := decode(&a); err != nil {
			return nil, err
		}
		if len(a.TaskIDs) == 0 {
			return nil, errors.New("task_ids is empty")
		}
		failed := make(map[int32]bool, len(c.FailedSubtasks))
		for _, id := range c.FailedSubtasks {
			failed[id] = true
		}
		seen := map[int32]bool{}
		for _, id := range a.TaskIDs {
			if !failed[id] {
				return nil, fmt.Errorf("task %d is not a failed subtask of this task; retry only tasks listed as failed or canceled", id)
			}
			if seen[id] {
				return nil, fmt.Errorf("task %d is listed twice", id)
			}
			seen[id] = true
		}
		if strings.TrimSpace(a.Instructions) == "" {
			return nil, errors.New("instructions is empty: say what to do differently")
		}
		return a, validateDecisions(a.Decisions, true)
	case ToolFinishAdjustment:
		var a FinishAdjustment
		if err := decode(&a); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Reason) == "" {
			return nil, errors.New("reason is empty")
		}
		return a, validateDecisions(a.Decisions, true)
	case ToolEscalate:
		var a Escalate
		if err := decode(&a); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Reason) == "" {
			return nil, errors.New("reason is empty")
		}
		return a, validateDecisions(a.Decisions, true)
	case ToolFinishVerification:
		var a FinishVerification
		if err := decode(&a); err != nil {
			return nil, err
		}
		if len(a.Criteria) == 0 {
			return nil, errors.New("criteria is empty: judge every item of the definition of done")
		}
		for i, criterion := range a.Criteria {
			if strings.TrimSpace(criterion.Criterion) == "" {
				return nil, fmt.Errorf("criteria[%d].criterion is empty", i)
			}
			if a.Passed && !criterion.Passed {
				return nil, fmt.Errorf("passed is true but criteria[%d] is not met; a verdict passes only if every criterion does", i)
			}
		}
		if strings.TrimSpace(a.Summary) == "" {
			return nil, errors.New("summary is empty")
		}
		return a, validateDecisions(a.Decisions, true)
	case ToolGetDecisionTree:
		var a GetDecisionTree
		if err := decode(&a); err != nil {
			return nil, err
		}
		if a.Scope == "" {
			a.Scope = "task"
		}
		if a.Scope != "task" && a.Scope != "root" {
			return nil, errors.New(`scope must be "task" or "root"`)
		}
		return a, nil
	case ToolGetExecutionState:
		var a GetExecutionState
		if err := decode(&a); err != nil {
			return nil, err
		}
		return a, nil
	}
	return nil, fmt.Errorf("unknown tool %s", tool)
}

func cleanList(field string, items []string) ([]string, error) {
	cleaned := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("%s must contain at least one item", field)
	}
	return cleaned, nil
}

func validateDecisions(decisions []DecisionInput, required bool) error {
	if required && len(decisions) == 0 {
		return errors.New("decisions is empty: record what you decided in this step and why")
	}
	for i, decision := range decisions {
		if strings.TrimSpace(decision.Title) == "" {
			return fmt.Errorf("decisions[%d].title is empty", i)
		}
		if strings.TrimSpace(decision.Decision) == "" {
			return fmt.Errorf("decisions[%d].decision is empty", i)
		}
		if strings.TrimSpace(decision.Rationale) == "" {
			return fmt.Errorf("decisions[%d].rationale is empty: say why", i)
		}
	}
	return nil
}

func validatePlannedTasks(tasks []PlannedTask, c ToolContext) error {
	if len(tasks) == 0 {
		return errors.New("tasks must contain at least one task")
	}
	if len(tasks) > c.Budgets.MaxTasksPerStep {
		return fmt.Errorf("at most %d tasks per step; group the work or plan it in stages", c.Budgets.MaxTasksPerStep)
	}
	roles := make(map[string]bool, len(c.Roles))
	for _, role := range c.Roles {
		roles[strings.ToLower(role)] = true
	}
	index := make(map[string]int, len(tasks))
	for i := range tasks {
		task := &tasks[i]
		task.Key = strings.TrimSpace(task.Key)
		if task.Key == "" {
			return fmt.Errorf("tasks[%d].key is empty", i)
		}
		if _, duplicate := index[task.Key]; duplicate {
			return fmt.Errorf("task key %q is used twice", task.Key)
		}
		index[task.Key] = i
		if strings.TrimSpace(task.Title) == "" {
			return fmt.Errorf("task %q has no title", task.Key)
		}
		if strings.TrimSpace(task.Instructions) == "" {
			return fmt.Errorf("task %q has no instructions", task.Key)
		}
		if strings.TrimSpace(task.DoneWhen) == "" {
			return fmt.Errorf("task %q has no done_when", task.Key)
		}
		switch task.Type {
		case models.TaskTypeCoding, models.TaskTypeResearch, models.TaskTypeReview, models.TaskTypeGeneral:
		default:
			return fmt.Errorf("task %q has type %q; use coding, research, review or general", task.Key, task.Type)
		}
		if task.Role != "" && len(roles) > 0 && !roles[strings.ToLower(task.Role)] {
			return fmt.Errorf("task %q names role %q, which does not exist; available roles: %s", task.Key, task.Role, strings.Join(c.Roles, ", "))
		}
		if task.Managed && !c.managedSubtasksAllowed() {
			return fmt.Errorf("task %q cannot be managed at this depth; make it small enough for one executor session", task.Key)
		}
	}
	for _, task := range tasks {
		for _, dependency := range task.DependsOn {
			if dependency == task.Key {
				return fmt.Errorf("task %q depends on itself", task.Key)
			}
			if _, ok := index[dependency]; !ok {
				return fmt.Errorf("task %q depends on %q, which is not a key in this list", task.Key, dependency)
			}
		}
	}
	// A dependency cycle would leave every task in it waiting forever.
	const (
		unvisited = iota
		visiting
		finished
	)
	state := make([]int, len(tasks))
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case finished:
			return nil
		case visiting:
			return fmt.Errorf("the tasks form a dependency cycle through %q", tasks[i].Key)
		}
		state[i] = visiting
		for _, dependency := range tasks[i].DependsOn {
			if err := visit(index[dependency]); err != nil {
				return err
			}
		}
		state[i] = finished
		return nil
	}
	for i := range tasks {
		if err := visit(i); err != nil {
			return err
		}
	}
	return nil
}

// sameQuestion reports whether two questions ask the same thing: equal once
// case, punctuation and spacing are ignored, or sharing most of their words.
// Models rephrase a question they have asked before far more often than they
// repeat it letter for letter. In practice rewordings share well over 0.6 of
// their words and different questions about one task under 0.3, so the line
// is drawn where neither is close.
func sameQuestion(a, b string) bool {
	wordsA, wordsB := questionWords(a), questionWords(b)
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return false
	}
	shared := 0
	for word := range wordsA {
		if wordsB[word] {
			shared++
		}
	}
	union := len(wordsA) + len(wordsB) - shared
	return float64(shared)/float64(union) >= sameQuestionOverlap
}

const sameQuestionOverlap = 0.6

func questionWords(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		words[word] = true
	}
	return words
}
