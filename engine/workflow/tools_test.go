package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"agent-orchestrator/db/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var allPhases = []string{
	models.TaskPhaseRefine, models.TaskPhaseDesign, models.TaskPhaseTestPlan, models.TaskPhasePlan,
	models.TaskPhaseExecute, models.TaskPhaseAdjust, models.TaskPhaseVerify,
}

func toolNames(specs []ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func testContext() ToolContext {
	return ToolContext{Budgets: DefaultBudgets, Roles: []string{"CEO", "CTO", "Coder", "QA"}, FailedSubtasks: []int32{7, 8}}
}

func TestToolSetsPerPhase(t *testing.T) {
	c := testContext()
	assert.Equal(t, []string{ToolAskQuestions, ToolAskHuman, ToolFinishRefinement}, toolNames(ToolsFor(models.TaskPhaseRefine, c)))
	assert.Equal(t, []string{ToolAskQuestions, ToolAskHuman, ToolFinishDesign}, toolNames(ToolsFor(models.TaskPhaseDesign, c)))
	assert.Equal(t, []string{ToolAskQuestions, ToolAskHuman, ToolFinishTestPlan}, toolNames(ToolsFor(models.TaskPhaseTestPlan, c)))
	assert.Equal(t, []string{ToolAskQuestions, ToolAskHuman, ToolCreateTasks}, toolNames(ToolsFor(models.TaskPhasePlan, c)))
	assert.Equal(t, []string{ToolGetDecisionTree, ToolGetExecutionState, ToolAskQuestions, ToolAskHuman,
		ToolCreateTasks, ToolRetryTasks, ToolFinishAdjustment, ToolEscalate}, toolNames(ToolsFor(models.TaskPhaseAdjust, c)))
	assert.Equal(t, []string{ToolGetExecutionState, ToolAskQuestions, ToolFinishVerification}, toolNames(ToolsFor(models.TaskPhaseVerify, c)))
	assert.Empty(t, ToolsFor(models.TaskPhaseExecute, c), "execution is not a smart phase")
}

// A smart model may not touch files or the outside world. The complete set of
// tools it can ever be offered is this closed list: asking, deciding,
// delegating, and reading the workflow's own records.
func TestSmartToolsAreAClosedSet(t *testing.T) {
	allowed := map[string]bool{
		ToolAskQuestions: true, ToolAskHuman: true,
		ToolFinishRefinement: true, ToolFinishDesign: true, ToolFinishTestPlan: true,
		ToolCreateTasks: true, ToolRetryTasks: true, ToolFinishAdjustment: true, ToolEscalate: true,
		ToolFinishVerification: true, ToolGetDecisionTree: true, ToolGetExecutionState: true,
	}
	offered := map[string]bool{}
	for _, phase := range allPhases {
		for _, spec := range ToolsFor(phase, testContext()) {
			assert.True(t, allowed[spec.Name], "%s offered in %s is not a workflow tool", spec.Name, phase)
			offered[spec.Name] = true
		}
	}
	assert.Len(t, offered, len(allowed), "every workflow tool is offered in some phase")
}

func TestReadOnlyToolsDropOutWhenTheirBudgetIsSpent(t *testing.T) {
	c := testContext()
	c.InspectsUsed = c.Budgets.MaxInspectsPerPhase - 1
	assert.Contains(t, toolNames(ToolsFor(models.TaskPhaseAdjust, c)), ToolGetDecisionTree)

	c.InspectsUsed = c.Budgets.MaxInspectsPerPhase
	adjust := toolNames(ToolsFor(models.TaskPhaseAdjust, c))
	assert.NotContains(t, adjust, ToolGetDecisionTree)
	assert.NotContains(t, adjust, ToolGetExecutionState)
	assert.Contains(t, adjust, ToolCreateTasks, "the acting tools remain, so the model has to act")
	assert.NotContains(t, toolNames(ToolsFor(models.TaskPhaseVerify, c)), ToolGetExecutionState)

	_, err := ParseAction(models.TaskPhaseAdjust, ToolGetDecisionTree, json.RawMessage(`{"scope":"task"}`), c)
	require.Error(t, err, "a spent tool is also refused if the model calls it anyway")
}

func TestRetryIsOfferedOnlyWhenSomethingFailed(t *testing.T) {
	c := testContext()
	c.FailedSubtasks = nil
	assert.NotContains(t, toolNames(ToolsFor(models.TaskPhaseAdjust, c)), ToolRetryTasks)
}

func TestEveryToolSchemaIsValidJSONWithAnObjectRoot(t *testing.T) {
	for _, phase := range allPhases {
		for _, spec := range ToolsFor(phase, testContext()) {
			var schema map[string]interface{}
			require.NoError(t, json.Unmarshal(spec.Parameters, &schema), "%s: %s", spec.Name, spec.Parameters)
			assert.Equal(t, "object", schema["type"], spec.Name)
			assert.NotEmpty(t, spec.Description, spec.Name)
			required, ok := schema["required"].([]interface{})
			require.True(t, ok, "%s: required must be an array", spec.Name)
			properties := schema["properties"].(map[string]interface{})
			for _, name := range required {
				assert.Contains(t, properties, name, "%s requires an undeclared property", spec.Name)
			}
			if !IsInspectTool(spec.Name) {
				assert.Contains(t, properties, "decisions", "%s must carry decisions", spec.Name)
				assert.Contains(t, required, "decisions", "%s must require decisions", spec.Name)
			}
		}
	}
}

// State-changing tools that commit to something require at least one
// decision; asking a question does not, since nothing has been decided yet.
func TestWhichToolsDemandADecision(t *testing.T) {
	demands := func(phase, tool string) bool {
		for _, spec := range ToolsFor(phase, testContext()) {
			if spec.Name == tool {
				return strings.Contains(string(spec.Parameters), `"minItems": 1`)
			}
		}
		t.Fatalf("%s not offered in %s", tool, phase)
		return false
	}
	assert.False(t, demands(models.TaskPhaseRefine, ToolAskQuestions))
	assert.False(t, demands(models.TaskPhaseRefine, ToolAskHuman))
	assert.True(t, demands(models.TaskPhaseRefine, ToolFinishRefinement))
	assert.True(t, demands(models.TaskPhaseDesign, ToolFinishDesign))
	assert.True(t, demands(models.TaskPhaseTestPlan, ToolFinishTestPlan))
	assert.True(t, demands(models.TaskPhasePlan, ToolCreateTasks))
	assert.True(t, demands(models.TaskPhaseAdjust, ToolRetryTasks))
	assert.True(t, demands(models.TaskPhaseAdjust, ToolFinishAdjustment))
	assert.True(t, demands(models.TaskPhaseAdjust, ToolEscalate))
	assert.True(t, demands(models.TaskPhaseVerify, ToolFinishVerification))
}

func TestCreateTasksSchemaReflectsRolesAndDepth(t *testing.T) {
	spec := func(c ToolContext) string {
		for _, s := range ToolsFor(models.TaskPhasePlan, c) {
			if s.Name == ToolCreateTasks {
				return string(s.Parameters)
			}
		}
		return ""
	}
	c := testContext()
	assert.Contains(t, spec(c), `"enum":["CEO","CTO","Coder","QA"]`)
	assert.Contains(t, spec(c), `"managed"`)

	c.Depth = c.Budgets.MaxManagedDepth
	assert.NotContains(t, spec(c), `"managed"`, "too deep for a subtask to be managed")
	c.Roles = nil
	assert.NotContains(t, spec(c), `"enum":["CEO"`)
}

const oneDecision = `"decisions":[{"title":"t","decision":"d","rationale":"r"}]`

func TestParseActionAcceptsWellFormedAnswers(t *testing.T) {
	c := testContext()
	cases := []struct {
		phase, tool, args string
		check             func(*testing.T, Action)
	}{
		{models.TaskPhaseRefine, ToolAskQuestions, `{"questions":[{"question":" How is auth done? ","context":"see server/"},{"question":"Is it tested?","kind":"review"}],"decisions":[]}`,
			func(t *testing.T, a Action) {
				ask := a.(AskQuestions)
				require.Len(t, ask.Questions, 2)
				assert.Equal(t, "How is auth done?", ask.Questions[0].Question)
				assert.Equal(t, models.TaskTypeResearch, ask.Questions[0].Kind, "kind defaults to research")
				assert.Equal(t, models.TaskTypeReview, ask.Questions[1].Kind)
			}},
		{models.TaskPhasePlan, ToolAskHuman, `{"question":"Which region?","why":"it decides the provider","decisions":[]}`,
			func(t *testing.T, a Action) { assert.Equal(t, "Which region?", a.(AskHuman).Question) }},
		{models.TaskPhaseRefine, ToolFinishRefinement, `{"spec":"Do X","definition_of_done":[" a ","","b"],` + oneDecision + `}`,
			func(t *testing.T, a Action) {
				assert.Equal(t, []string{"a", "b"}, a.(FinishRefinement).DefinitionOfDone, "blank items are dropped")
				require.Len(t, a.Recorded(), 1)
			}},
		{models.TaskPhaseDesign, ToolFinishDesign, `{"design":"Use a queue",` + oneDecision + `}`, nil},
		{models.TaskPhaseTestPlan, ToolFinishTestPlan, `{"test_scenarios":["login works"],` + oneDecision + `}`, nil},
		{models.TaskPhasePlan, ToolCreateTasks, `{"tasks":[
			{"key":"db","title":"Schema","instructions":"add table","done_when":"migrates","type":"coding"},
			{"key":"api","title":"API","instructions":"add route","done_when":"tests pass","type":"coding","role":"coder","depends_on":["db"]},
			{"key":"big","title":"Subsystem","instructions":"build it","done_when":"works","type":"general","managed":true}],` + oneDecision + `}`,
			func(t *testing.T, a Action) {
				tasks := a.(CreateTasks).Tasks
				require.Len(t, tasks, 3)
				assert.Equal(t, []string{"db"}, tasks[1].DependsOn)
				assert.True(t, tasks[2].Managed)
			}},
		{models.TaskPhaseAdjust, ToolRetryTasks, `{"task_ids":[7],"instructions":"use the v2 API",` + oneDecision + `}`, nil},
		{models.TaskPhaseAdjust, ToolFinishAdjustment, `{"reason":"the failed part was optional",` + oneDecision + `}`, nil},
		{models.TaskPhaseAdjust, ToolEscalate, `{"reason":"no access to prod",` + oneDecision + `}`, nil},
		{models.TaskPhaseVerify, ToolFinishVerification, `{"passed":true,"criteria":[{"criterion":"a","passed":true,"note":"seen"}],"summary":"done",` + oneDecision + `}`, nil},
		{models.TaskPhaseVerify, ToolFinishVerification, `{"passed":false,"criteria":[{"criterion":"a","passed":false,"note":"missing"}],"summary":"redo a",` + oneDecision + `}`, nil},
		{models.TaskPhaseAdjust, ToolGetDecisionTree, `{}`,
			func(t *testing.T, a Action) {
				assert.Equal(t, "task", a.(GetDecisionTree).Scope, "scope defaults to task")
			}},
		{models.TaskPhaseAdjust, ToolGetExecutionState, ``,
			func(t *testing.T, a Action) { assert.Nil(t, a.Recorded()) }},
	}
	for _, tc := range cases {
		t.Run(tc.phase+"/"+tc.tool, func(t *testing.T) {
			action, err := ParseAction(tc.phase, tc.tool, json.RawMessage(tc.args), c)
			require.NoError(t, err)
			assert.Equal(t, tc.tool, action.Tool())
			if tc.check != nil {
				tc.check(t, action)
			}
		})
	}
}

func TestParseActionRejectsWithAReasonTheModelCanActOn(t *testing.T) {
	c := testContext()
	tooMany := `{"questions":[` + strings.TrimSuffix(strings.Repeat(`{"question":"q"},`, c.Budgets.MaxQuestionsPerStep+1), ",") + `],"decisions":[]}`
	task := func(extra string) string {
		return `{"key":"a","title":"A","instructions":"i","done_when":"d","type":"coding"` + extra + `}`
	}
	cases := []struct {
		name, phase, tool, args, want string
	}{
		{"wrong phase", models.TaskPhaseRefine, ToolCreateTasks, `{}`, "not available in the refine phase"},
		{"no file tools", models.TaskPhaseRefine, "read_file", `{}`, "not available"},
		{"bad json", models.TaskPhaseRefine, ToolFinishRefinement, `{"spec":`, "not valid JSON"},
		{"no questions", models.TaskPhaseRefine, ToolAskQuestions, `{"questions":[],"decisions":[]}`, "at least one question"},
		{"blank question", models.TaskPhaseRefine, ToolAskQuestions, `{"questions":[{"question":"  "}],"decisions":[]}`, "questions[0].question is empty"},
		{"too many questions", models.TaskPhaseRefine, ToolAskQuestions, tooMany, "at most 8 questions"},
		{"bad question kind", models.TaskPhaseRefine, ToolAskQuestions, `{"questions":[{"question":"q","kind":"coding"}],"decisions":[]}`, "must be research or review"},
		{"human without why", models.TaskPhaseRefine, ToolAskHuman, `{"question":"q","why":"","decisions":[]}`, "why this needs the human"},
		{"no spec", models.TaskPhaseRefine, ToolFinishRefinement, `{"spec":" ","definition_of_done":["a"],` + oneDecision + `}`, "spec is empty"},
		{"no criteria", models.TaskPhaseRefine, ToolFinishRefinement, `{"spec":"s","definition_of_done":[" "],` + oneDecision + `}`, "definition_of_done must contain"},
		{"no decisions", models.TaskPhaseRefine, ToolFinishRefinement, `{"spec":"s","definition_of_done":["a"],"decisions":[]}`, "record what you decided"},
		{"decision without rationale", models.TaskPhaseRefine, ToolFinishRefinement, `{"spec":"s","definition_of_done":["a"],"decisions":[{"title":"t","decision":"d","rationale":""}]}`, "rationale is empty"},
		{"no tasks", models.TaskPhasePlan, ToolCreateTasks, `{"tasks":[],` + oneDecision + `}`, "at least one task"},
		{"duplicate key", models.TaskPhasePlan, ToolCreateTasks, `{"tasks":[` + task("") + `,` + task("") + `],` + oneDecision + `}`, `"a" is used twice`},
		{"unknown dependency", models.TaskPhasePlan, ToolCreateTasks, `{"tasks":[` + task(`,"depends_on":["zzz"]`) + `],` + oneDecision + `}`, `"zzz", which is not a key`},
		{"self dependency", models.TaskPhasePlan, ToolCreateTasks, `{"tasks":[` + task(`,"depends_on":["a"]`) + `],` + oneDecision + `}`, "depends on itself"},
		{"cycle", models.TaskPhasePlan, ToolCreateTasks, `{"tasks":[
			{"key":"a","title":"A","instructions":"i","done_when":"d","type":"coding","depends_on":["b"]},
			{"key":"b","title":"B","instructions":"i","done_when":"d","type":"coding","depends_on":["a"]}],` + oneDecision + `}`, "dependency cycle"},
		{"bad type", models.TaskPhasePlan, ToolCreateTasks, `{"tasks":[{"key":"a","title":"A","instructions":"i","done_when":"d","type":"deploy"}],` + oneDecision + `}`, "use coding, research, review or general"},
		{"unknown role", models.TaskPhasePlan, ToolCreateTasks, `{"tasks":[` + task(`,"role":"Wizard"`) + `],` + oneDecision + `}`, "available roles: CEO, CTO, Coder, QA"},
		{"no done_when", models.TaskPhasePlan, ToolCreateTasks, `{"tasks":[{"key":"a","title":"A","instructions":"i","done_when":"","type":"coding"}],` + oneDecision + `}`, "has no done_when"},
		{"retry a task that did not fail", models.TaskPhaseAdjust, ToolRetryTasks, `{"task_ids":[99],"instructions":"x",` + oneDecision + `}`, "task 99 is not a failed subtask"},
		{"retry listed twice", models.TaskPhaseAdjust, ToolRetryTasks, `{"task_ids":[7,7],"instructions":"x",` + oneDecision + `}`, "listed twice"},
		{"retry without change", models.TaskPhaseAdjust, ToolRetryTasks, `{"task_ids":[7],"instructions":"",` + oneDecision + `}`, "what to do differently"},
		{"verdict without criteria", models.TaskPhaseVerify, ToolFinishVerification, `{"passed":true,"criteria":[],"summary":"s",` + oneDecision + `}`, "judge every item"},
		{"pass with an unmet criterion", models.TaskPhaseVerify, ToolFinishVerification, `{"passed":true,"criteria":[{"criterion":"a","passed":false,"note":"n"}],"summary":"s",` + oneDecision + `}`, "passes only if every criterion does"},
		{"bad scope", models.TaskPhaseAdjust, ToolGetDecisionTree, `{"scope":"company"}`, `"task" or "root"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAction(tc.phase, tc.tool, json.RawMessage(tc.args), c)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	deep := c
	deep.Depth = c.Budgets.MaxManagedDepth
	_, err := ParseAction(models.TaskPhasePlan, ToolCreateTasks, json.RawMessage(`{"tasks":[`+task(`,"managed":true`)+`],`+oneDecision+`}`), deep)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be managed at this depth")
}

func TestTemplates(t *testing.T) {
	coding := TemplateFor(models.TaskTypeCoding)
	assert.Equal(t, models.TaskPhaseRefine, coding.First())
	path := []string{coding.First()}
	for {
		next, ok := coding.After(path[len(path)-1])
		if !ok {
			break
		}
		path = append(path, next)
	}
	assert.Equal(t, []string{"refine", "design", "test_plan", "plan", "execute", "verify"}, path)
	assert.Equal(t, RoleCTO, coding.RoleFor(models.TaskPhaseDesign))
	assert.Equal(t, RoleQALead, coding.RoleFor(models.TaskPhaseTestPlan))
	assert.Equal(t, RoleCTO, coding.RoleFor(models.TaskPhaseAdjust))
	assert.Empty(t, coding.RoleFor(models.TaskPhaseRefine), "the task's own agent refines")
	assert.True(t, coding.ReviewImplementation)

	for _, taskType := range []string{models.TaskTypeResearch, models.TaskTypeReview, models.TaskTypeGeneral, "unknown"} {
		template := TemplateFor(taskType)
		assert.Equal(t, []string{"refine", "plan", "execute", "verify"}, template.Phases, taskType)
		assert.False(t, template.ReviewImplementation, taskType)
		_, hasNext := template.After(models.TaskPhaseVerify)
		assert.False(t, hasNext)
	}
	_, ok := coding.After(models.TaskPhaseAdjust)
	assert.False(t, ok, "adjust is not on the forward path")

	assert.True(t, WritesWorkspace(models.TaskTypeCoding))
	assert.True(t, WritesWorkspace(models.TaskTypeGeneral))
	assert.False(t, WritesWorkspace(models.TaskTypeResearch))
	assert.False(t, WritesWorkspace(models.TaskTypeReview))
}
