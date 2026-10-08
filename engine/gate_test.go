package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/classifier"
	"agent-orchestrator/engine/workflow"
	"agent-orchestrator/pkg/secrets"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeJev stands in for the TypeSafe API. Each question is answered by the
// test's judge; what it was asked is kept.
type fakeJev struct {
	mu       sync.Mutex
	states   []string
	asked    [][]string
	status   int
	judge    func(state, name, instructions string, options map[string]string) classifier.Answer
	server   *httptest.Server
	provider db.LLMProvider
}

func (j *fakeJev) serve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State     string `json:"state"`
		Questions map[string]struct {
			Type         string            `json:"type"`
			Instructions string            `json:"instructions"`
			Criteria     map[string]string `json:"criteria"`
		} `json:"questions"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.states = append(j.states, body.State)
	var names []string
	answers := map[string]classifier.Answer{}
	for name, question := range body.Questions {
		names = append(names, name)
		answers[name] = j.judge(body.State, name, question.Instructions, question.Criteria)
	}
	j.asked = append(j.asked, names)
	if j.status != 0 {
		w.WriteHeader(j.status)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"model": "jev-test", "answers": answers, "usage": map[string]int{"input_tokens": 50, "output_tokens": 1},
	})
}

func (j *fakeJev) requests() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.states)
}

// useClassifier configures the company's classifier slot with a fake Jev.
func (f *driverFixture) useClassifier(judge func(state, name, instructions string, options map[string]string) classifier.Answer) *fakeJev {
	f.t.Helper()
	j := &fakeJev{judge: judge}
	j.server = httptest.NewServer(http.HandlerFunc(j.serve))
	f.t.Cleanup(j.server.Close)
	sealed, err := secrets.Default().EncryptForUser(f.owner.ID, "jev-key")
	require.NoError(f.t, err)
	j.provider = db.LLMProvider{Name: "TypeSafe", BaseUrl: j.server.URL, ApiKeyEncrypted: sealed, UserID: &f.owner.ID, ProviderType: classifier.ProviderType, Enabled: true}
	require.NoError(f.t, f.database.Create(&j.provider).Error)
	_, err = f.q.UpdateDefaultModelSetting(context.Background(), f.owner.ID, db.PurposeClassifier, &j.provider.ID, classifier.DefaultModel, nil)
	require.NoError(f.t, err)
	return j
}

func (f *driverFixture) classifierCalls(purpose string) []db.LLMCall {
	calls, err := f.q.ListLLMCalls(context.Background(), db.UsageFilter{CompanyID: f.company.ID, Tier: models.TierClassifier}, 0, 500)
	require.NoError(f.t, err)
	var matching []db.LLMCall
	for _, call := range calls {
		if call.Purpose == purpose {
			matching = append(matching, call)
		}
	}
	return matching
}

var nothingNotable = func(string, string, string, map[string]string) classifier.Answer {
	return classifier.Answer{Noul: 0.02}
}

// The classifier sees an approach being abandoned and the engine asks for a
// checkpoint at that moment, long before the fixed interval would.
func TestClassifierFlagsATurnAndTheCheckpointComesAtOnce(t *testing.T) {
	x := newExecutorFixture(t)
	jev := x.useClassifier(func(state, name, _ string, _ map[string]string) classifier.Answer {
		if name == "abandoned" && strings.Contains(state, "grep") {
			return classifier.Answer{Noul: 0.93}
		}
		return classifier.Answer{Noul: 0.03}
	})
	task := x.direct(models.TaskTypeResearch, "Find the login handler", "Locate it.")
	x.provider.pushCheap(
		call("ls", `{"path":"."}`),
		call("grep", `{"pattern":"login","path":"."}`),
		// Demanded after two tool calls; the interval is twelve.
		call("checkpoint", `{"progress":"Grep was useless","next_step":"read the router",
			"dead_ends":[{"title":"Grep for login","detail":"Searched every file for 'login'","reason":"no matches"}]}`),
		finish("done", "Found it in the router."),
	)

	x.start(task.ID)
	require.Equal(t, db.TaskStatusDone, x.task(task.ID).Status)

	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 4)
	assert.Greater(t, len(toolsOffered(requests[1])), 5, "nothing notable after the first call: no checkpoint")
	assert.Equal(t, []string{"checkpoint"}, toolsOffered(requests[2]), "the abandoned approach is recorded while it is fresh")
	assert.Contains(t, toolDefinition(requests[2], "checkpoint"), `"dead_ends"`, "something notable happened: the records are asked for")

	decisions, err := x.q.ListDecisionsByTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, models.DecisionKindDeadEnd, decisions[0].Kind)

	// The classifier was shown what the agent did, and its calls are billed.
	assert.Contains(t, strings.Join(jev.states, "\n"), `CALL grep({"pattern":"login","path":"."})`)
	gateCalls := x.classifierCalls("checkpoint_gate")
	require.NotEmpty(t, gateCalls)
	assert.Equal(t, 50, gateCalls[0].PromptTokens)
	assert.Equal(t, "jev-test", gateCalls[0].Model)
	require.NotNil(t, gateCalls[0].RunID)
	assert.Equal(t, task.ID, *gateCalls[0].TaskID)
}

// When the interval is up and the classifier has seen nothing worth recording,
// the checkpoint asks only where the work stands. With no decision fields on
// offer there is nothing for the model to restate.
func TestClassifierSeesNothingNotableSoTheCheckpointAsksOnlyForProgress(t *testing.T) {
	x := newExecutorFixture(t)
	x.engine.driver.budgets.CheckpointEveryToolCalls = 3
	x.useClassifier(nothingNotable)
	task := x.direct(models.TaskTypeResearch, "Survey the code", "Look around and report.")
	x.provider.pushCheap(
		call("ls", `{"path":"."}`), call("ls", `{"path":"a"}`), call("ls", `{"path":"b"}`),
		call("checkpoint", `{"progress":"Listed three directories","next_step":"read them"}`),
		finish("done", "Surveyed."),
	)

	x.start(task.ID)
	require.Equal(t, db.TaskStatusDone, x.task(task.ID).Status)

	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 5)
	assert.Equal(t, []string{"checkpoint"}, toolsOffered(requests[3]))
	definition := toolDefinition(requests[3], "checkpoint")
	assert.Contains(t, definition, `"progress"`)
	assert.NotContains(t, definition, `"decisions"`)
	assert.NotContains(t, definition, `"dead_ends"`)
	assert.NotContains(t, lastUserMessage(requests[3]), "Already on record")
	// The full schema is back for a checkpoint the model volunteers later.
	assert.Contains(t, toolDefinition(requests[4], "checkpoint"), `"decisions"`)
}

// A model made to checkpoint tends to say again what it said before, in other
// words. The classifier recognises the restatement and nothing is added.
func TestClassifierCatchesARecordRestatedInOtherWords(t *testing.T) {
	x := newExecutorFixture(t)
	x.useClassifier(func(state, name, instructions string, options map[string]string) classifier.Answer {
		if strings.HasPrefix(name, "entry") && strings.Contains(instructions, "server-side sessions") {
			for label, text := range options {
				if strings.Contains(text, "Use sessions") {
					return classifier.Answer{Choice: label, Confidence: 0.91}
				}
			}
		}
		if strings.HasPrefix(name, "entry") {
			return classifier.Answer{Choice: "none", Confidence: 0.88}
		}
		return classifier.Answer{Noul: 0.02}
	})
	task := x.direct(models.TaskTypeResearch, "Decide on auth", "Pick an approach.")
	x.provider.pushCheap(
		call("checkpoint", `{"progress":"Compared options","next_step":"write up",
			"decisions":[{"title":"Use sessions","detail":"Keep login state in server sessions","reason":"simple to revoke"}]}`),
		call("finish_work", `{"status":"done","summary":"Decided.",
			"decisions":[
				{"title":"Go with server-side sessions","detail":"Login state lives on the server","reason":"revocation is easy"},
				{"title":"Expire after a day","detail":"Sessions last 24 hours","reason":"a common default"}]}`),
	)

	x.start(task.ID)
	require.Equal(t, db.TaskStatusDone, x.task(task.ID).Status)

	decisions, err := x.q.ListDecisionsByTask(context.Background(), task.ID)
	require.NoError(t, err)
	var titles []string
	for _, decision := range decisions {
		titles = append(titles, decision.Title)
	}
	assert.Equal(t, []string{"Use sessions", "Expire after a day"}, titles, "the restatement is not a second record; the new decision is kept")

	var notes []string
	for _, step := range x.steps(task.ID) {
		if step.Kind == models.StepNote {
			notes = append(notes, step.Result)
		}
	}
	assert.Equal(t, []string{"duplicate of #1: Go with server-side sessions"}, notes, "what was skipped is on the journal")
	assert.NotEmpty(t, x.classifierCalls("record_dedupe"))
}

// A session that keeps doing the same thing is stopped and made to say where
// it got stuck, instead of burning turns until its limit.
func TestClassifierSeesALoopTwiceAndTheSessionIsMadeToReport(t *testing.T) {
	x := newExecutorFixture(t)
	x.useClassifier(func(_, name, _ string, _ map[string]string) classifier.Answer {
		if name == "looping" {
			return classifier.Answer{Noul: 0.95}
		}
		return classifier.Answer{Noul: 0.02}
	})
	task := x.direct(models.TaskTypeResearch, "Fetch the report", "Download it.")
	x.provider.pushCheap(
		call("web_fetch", `{"url":"http://127.0.0.1:1/a"}`),
		call("web_fetch", `{"url":"http://127.0.0.1:1/b"}`),
		// Two turns in a row looked like a loop: only finish_work is offered.
		call("finish_work", `{"status":"cannot_complete","summary":"The host refuses every connection.","details":"Tried /a and /b; both refused."}`),
	)

	x.start(task.ID)

	failed := x.task(task.ID)
	assert.Equal(t, db.TaskStatusFailed, failed.Status)
	assert.Equal(t, models.TaskResultCannotComplete, failed.ResultReason)
	assert.Equal(t, "The host refuses every connection.", failed.ResultSummary)
	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 3)
	assert.Greater(t, len(toolsOffered(requests[1])), 5, "one hit is not a loop yet")
	assert.Equal(t, []string{"finish_work"}, toolsOffered(requests[2]))
	assert.Equal(t, aicli.ToolChoiceRequired, requests[2].ToolChoice)
	assert.Contains(t, lastUserMessage(requests[2]), "You are repeating the same actions")
}

// The same call made over and over is a loop on its face; no classifier is
// needed to see it.
func TestIdenticalCallsInARowStopTheSessionWithoutAClassifier(t *testing.T) {
	x := newExecutorFixture(t)
	task := x.direct(models.TaskTypeResearch, "List forever", "List the directory.")
	same := call("ls", `{"path":"."}`)
	x.provider.pushCheap(same, same, same, same, same, same,
		call("finish_work", `{"status":"failed","summary":"Listing the directory told me nothing new."}`))

	x.start(task.ID)

	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 7)
	assert.Greater(t, len(toolsOffered(requests[5])), 5)
	assert.Equal(t, []string{"finish_work"}, toolsOffered(requests[6]), "six identical calls in a row")
	assert.Equal(t, db.TaskStatusFailed, x.task(task.ID).Status)
}

// The classifier is an optimisation. When it fails, or the slot points at
// something that is not a classifier, sessions run exactly as without one.
func TestAFailingOrMisconfiguredClassifierChangesNothing(t *testing.T) {
	x := newExecutorFixture(t)
	x.engine.driver.budgets.CheckpointEveryToolCalls = 2
	jev := x.useClassifier(nothingNotable)
	jev.status = http.StatusInternalServerError
	task := x.direct(models.TaskTypeResearch, "Survey", "Look.")
	script := func() {
		x.provider.pushCheap(
			call("ls", `{"path":"."}`), call("ls", `{"path":"a"}`),
			call("checkpoint", `{"progress":"Listed","next_step":"report","decisions":[{"title":"Stop here","detail":"Two listings are enough","reason":"small repo"}]}`),
			finish("done", "Surveyed."),
		)
	}
	script()

	x.start(task.ID)
	require.Equal(t, db.TaskStatusDone, x.task(task.ID).Status)
	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 4)
	assert.Equal(t, []string{"checkpoint"}, toolsOffered(requests[2]), "the fixed interval still applies")
	assert.Greater(t, jev.requests(), 0)
	failures := x.classifierCalls("checkpoint_gate")
	require.NotEmpty(t, failures)
	assert.Equal(t, models.LLMCallError, failures[0].Status, "a failed classifier call is in the ledger as failed")

	// Point the slot at an ordinary model provider: it is not a classifier,
	// so it is never asked anything.
	_, err := x.q.UpdateDefaultModelSetting(context.Background(), x.owner.ID, db.PurposeClassifier, &x.llm.ID, cheapModel, nil)
	require.NoError(t, err)
	asked := jev.requests()
	second := x.direct(models.TaskTypeResearch, "Survey again", "Look.")
	script()
	x.start(second.ID)
	require.Equal(t, db.TaskStatusDone, x.task(second.ID).Status)
	assert.Equal(t, asked, jev.requests())
	assert.Len(t, x.provider.servedBy(cheapModel), 8, "and the ordinary provider was not sent classifier questions")
}

// A smart prompt whose context does not fit is not simply cut: the cheap
// model condenses what overflows, keeping task numbers, and the smart model
// is told that it is reading a condensed rendering.
func TestAnOverflowingPromptIsCondensedByTheCheapModel(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeResearch)
	long := strings.Repeat("The export endpoint returned 401 on every attempt. ", 2500) // ~125k characters
	f.executor = func(db.Task) (workflow.Report, bool) {
		return workflow.Report{Status: workflow.ReportDone, Summary: long}, true
	}
	f.provider.push(
		call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolCreateTasks, `{"tasks":[{"key":"a","title":"Export","instructions":"export","done_when":"exported","type":"research"}],`+oneDecisionArg+`}`),
		call(workflow.ToolFinishVerification, passArgs),
	)
	f.provider.pushCheap(scriptedReply{text: "Task 2 (Export): the endpoint answers 401 every time."})

	f.run(root.ID)
	require.Equal(t, db.TaskStatusInReview, f.task(root.ID).Status)

	compress := f.provider.servedBy(cheapModel)
	require.Len(t, compress, 1, "one cheap call condensed the one section that did not fit")
	assert.Contains(t, compress[0].Messages[0].Content, "You condense working notes")
	assert.Contains(t, compress[0].Messages[0].Content, `"Delegated work"`)
	assert.Contains(t, compress[0].Messages[1].Content, long[:500], "it was given the section in full")

	// The third smart request is the verification step.
	verify := f.provider.servedBy(smartModel)[2].Messages[1].Content
	assert.Contains(t, verify, "[Condensed to fit.")
	assert.Contains(t, verify, "Task 2 (Export): the endpoint answers 401 every time.")
	assert.NotContains(t, verify, "[Cut to fit.")
	assert.Less(t, len(verify), workflow.DefaultPromptBudget)

	// The condensing call is billed to the step it served.
	calls, err := f.q.ListLLMCalls(context.Background(), db.UsageFilter{CompanyID: f.company.ID, Tier: models.TierCheap}, 0, 50)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "compress", calls[0].Purpose)
	assert.Equal(t, models.TaskPhaseVerify, calls[0].Phase)
}

// With a classifier, reports that do not bear on the step are left out first.
// If the rest then fits, no model has to condense anything.
func TestTheClassifierLeavesOutReportsThatDoNotBearOnTheStep(t *testing.T) {
	f := newDriverFixture(t)
	jev := f.useClassifier(func(state, name, instructions string, _ map[string]string) classifier.Answer {
		// Find which report the question is about and judge it by its content.
		report := state[strings.Index(state, "["+name+"]")+len(name)+2:]
		if next := strings.Index(report, "\n[item"); next >= 0 {
			report = report[:next]
		}
		if strings.Contains(report, "Irrelevant tangent") {
			return classifier.Answer{Noul: 0.04}
		}
		return classifier.Answer{Noul: 0.9}
	})
	root := f.root(models.TaskTypeResearch)
	padding := strings.Repeat("Notes about office furniture. ", 3000) // ~90k characters
	f.executor = func(task db.Task) (workflow.Report, bool) {
		if task.Title == "Irrelevant tangent" {
			return workflow.Report{Status: workflow.ReportDone, Summary: padding}, true
		}
		return workflow.Report{Status: workflow.ReportDone, Summary: "The answer is 42."}, true
	}
	f.provider.push(
		call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolCreateTasks, `{"tasks":[
			{"key":"a","title":"Find the answer","instructions":"find","done_when":"found","type":"research"},
			{"key":"b","title":"Irrelevant tangent","instructions":"wander","done_when":"wandered","type":"research"}],`+oneDecisionArg+`}`),
		call(workflow.ToolFinishVerification, passArgs),
	)

	f.run(root.ID)
	require.Equal(t, db.TaskStatusInReview, f.task(root.ID).Status)

	// The third smart request is the verification step.
	verify := f.provider.servedBy(smartModel)[2].Messages[1].Content
	assert.Contains(t, verify, "The answer is 42.")
	assert.Contains(t, verify, "Irrelevant tangent", "the task is still listed")
	assert.Contains(t, verify, "Left out as not needed for this step")
	assert.NotContains(t, verify, "office furniture")
	assert.Empty(t, f.provider.servedBy(cheapModel), "what was left fits: nothing had to be condensed")
	assert.Equal(t, 1, jev.requests(), "one request judged every report")
	assert.Len(t, f.classifierCalls("relevance_gate"), 1)
}

// toolDefinition returns the schema a request offered for one tool.
func toolDefinition(request aicli.ChatRequest, name string) string {
	for _, tool := range request.Tools {
		if tool.Function.Name == name {
			return string(tool.Function.Parameters)
		}
	}
	return ""
}
