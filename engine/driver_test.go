package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/workflow"
	"agent-orchestrator/integration"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// scriptedReply is one answer of the fake provider.
type scriptedReply struct {
	tool   string
	args   string
	text   string
	status int
}

func call(tool, args string) scriptedReply { return scriptedReply{tool: tool, args: args} }

// fakeProvider is an OpenAI-compatible endpoint that answers from scripts,
// one per model, in order, and remembers every request it received.
type fakeProvider struct {
	t        *testing.T
	mu       sync.Mutex
	scripts  map[string][]scriptedReply
	requests []aicli.ChatRequest
	// tokens reported per answered request, so the ledger can be checked
	// against what the provider says it charged.
	promptTokens, completionTokens int
	// hold, when set, blocks each request until it is closed. holdModel
	// limits that to one model.
	hold      chan struct{}
	holdModel string
	// respond, when set, answers a request from its content instead of from
	// the script. Sessions that run at the same time need it: a shared queue
	// cannot tell them apart.
	respond func(request aicli.ChatRequest) (scriptedReply, bool)
	// inFlight and peak count requests being served at once, per model.
	inFlight map[string]int
	peak     map[string]int
	server   *httptest.Server
}

const (
	smartModel = "smart-model"
	cheapModel = "cheap-model"
)

func newFakeProvider(t *testing.T) *fakeProvider {
	p := &fakeProvider{t: t, promptTokens: 100, completionTokens: 10,
		scripts: map[string][]scriptedReply{}, inFlight: map[string]int{}, peak: map[string]int{}}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

// push scripts answers of the smart model; pushCheap of the cheap one.
func (p *fakeProvider) push(replies ...scriptedReply)      { p.pushFor(smartModel, replies...) }
func (p *fakeProvider) pushCheap(replies ...scriptedReply) { p.pushFor(cheapModel, replies...) }

func (p *fakeProvider) pushFor(model string, replies ...scriptedReply) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scripts[model] = append(p.scripts[model], replies...)
}

func (p *fakeProvider) served() []aicli.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]aicli.ChatRequest(nil), p.requests...)
}

// servedBy returns the requests one model received, in order.
func (p *fakeProvider) servedBy(model string) []aicli.ChatRequest {
	var requests []aicli.ChatRequest
	for _, request := range p.served() {
		if request.Model == model {
			requests = append(requests, request)
		}
	}
	return requests
}

func (p *fakeProvider) inFlightFor(model string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inFlight[model]
}

func (p *fakeProvider) peakFor(model string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak[model]
}

func (p *fakeProvider) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var request aicli.ChatRequest
	_ = json.Unmarshal(body, &request)
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.inFlight[request.Model]++
	if p.inFlight[request.Model] > p.peak[request.Model] {
		p.peak[request.Model] = p.inFlight[request.Model]
	}
	var hold chan struct{}
	if p.holdModel == "" || p.holdModel == request.Model {
		hold = p.hold
	}
	var reply scriptedReply
	scripted := false
	if p.respond != nil {
		reply, scripted = p.respond(request)
	}
	if script := p.scripts[request.Model]; !scripted && len(script) > 0 {
		reply, scripted = script[0], true
		p.scripts[request.Model] = script[1:]
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight[request.Model]--
		p.mu.Unlock()
	}()
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
	}
	if !scripted {
		p.t.Errorf("fake provider got a request with nothing scripted (model %s)", request.Model)
		w.WriteHeader(http.StatusTeapot)
		return
	}
	if reply.status != 0 {
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(`{"error":{"message":"scripted failure","type":"server_error"}}`))
		return
	}
	message := map[string]any{"role": "assistant", "content": reply.text}
	if reply.tool != "" {
		message["tool_calls"] = []map[string]any{{"id": fmt.Sprintf("call-%d", time.Now().UnixNano()), "type": "function",
			"function": map[string]any{"name": reply.tool, "arguments": reply.args}}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"model":   request.Model + "-served",
		"choices": []map[string]any{{"index": 0, "message": message}},
		"usage":   map[string]any{"prompt_tokens": p.promptTokens, "completion_tokens": p.completionTokens, "total_tokens": p.promptTokens + p.completionTokens},
	})
}

// userPrompt returns the user message of the nth request (0-based).
func (p *fakeProvider) userPrompt(n int) string {
	requests := p.served()
	require.Greater(p.t, len(requests), n, "the provider received only %d requests", len(requests))
	return requests[n].Messages[len(requests[n].Messages)-1].Content
}

func (p *fakeProvider) systemPrompt(n int) string {
	requests := p.served()
	require.Greater(p.t, len(requests), n)
	return requests[n].Messages[0].Content
}

// driverFixture is a database with a company set up to run the workflow, a
// driver, a fake provider behind the smart tier and a fake executor.
type driverFixture struct {
	t        *testing.T
	database *gorm.DB
	q        *db.Queries
	driver   *workflowDriver
	provider *fakeProvider
	basePath string
	owner    db.User
	company  db.Company
	sprint   db.Sprint
	agents   map[string]db.Agent
	llm      db.LLMProvider
	// executor decides how a direct task's session ends. Returning false
	// leaves the session running, as if it had not finished yet.
	executor func(task db.Task) (workflow.Report, bool)
	started  []int32
}

const oneDecisionArg = `"decisions":[{"title":"t","decision":"d","rationale":"r"}]`

func newDriverFixture(t *testing.T) *driverFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "driver.db")
	database, err := gorm.Open(sqlite.Open(path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))

	f := &driverFixture{t: t, database: database, q: db.New(database), provider: newFakeProvider(t),
		basePath: t.TempDir(), agents: map[string]db.Agent{}}
	ctx := context.Background()

	f.owner, err = f.q.CreateUser(ctx, fmt.Sprintf("owner-%d@example.com", time.Now().UnixNano()))
	require.NoError(t, err)
	f.unlock()
	sealed, err := secrets.Default().EncryptForUser(f.owner.ID, "test-key")
	require.NoError(t, err)
	f.llm = db.LLMProvider{Name: "fake", BaseUrl: f.provider.server.URL, ApiKeyEncrypted: sealed, UserID: &f.owner.ID, ProviderType: "openai", Enabled: true}
	require.NoError(t, database.Create(&f.llm).Error)
	require.NoError(t, f.q.EnsureDefaultModelSettingsForUser(ctx, f.owner.ID))
	f.setTier(db.PurposeSmart, smartModel)
	f.setTier(db.PurposeCheap, cheapModel)

	f.company = db.Company{Name: "HeadCount1", ShortName: "hc1", UserID: &f.owner.ID}
	require.NoError(t, database.Create(&f.company).Error)
	f.sprint = db.Sprint{CompanyID: f.company.ID, Name: "Sprint 1"}
	require.NoError(t, database.Create(&f.sprint).Error)
	for _, name := range []string{"CEO", "CTO", "Coder", "QA", "QA Lead"} {
		agent := db.Agent{CompanyID: f.company.ID, Name: name, RoleKey: name, ShortName: name, Builtin: true, Enabled: true,
			SystemPrompt: "You are the " + name + " agent."}
		require.NoError(t, database.Create(&agent).Error)
		f.agents[name] = agent
	}

	f.executor = func(db.Task) (workflow.Report, bool) {
		return workflow.Report{Status: workflow.ReportDone, Summary: "done"}, true
	}
	f.driver = f.newDriver()
	return f
}

func (f *driverFixture) unlock() {
	var dek [32]byte
	dek[0], dek[1] = 0x77, byte(f.owner.ID)
	secrets.Default().UnlockUser(f.owner.ID, dek, time.Hour)
}

func (f *driverFixture) setTier(purpose, model string) {
	_, err := f.q.UpdateDefaultModelSetting(context.Background(), f.owner.ID, purpose, &f.llm.ID, model, nil)
	require.NoError(f.t, err)
}

// newDriver builds a driver on the fixture's database. Calling it again gives
// a second, independent driver: what a restarted server would have.
func (f *driverFixture) newDriver() *workflowDriver {
	d := newWorkflowDriver(f.q, nil)
	d.basePath = func() string { return f.basePath }
	d.newClient = func(baseURL, apiKey, model string) *aicli.Client {
		client := aicli.NewClient(baseURL, apiKey, model)
		client.MaxRetries = 0
		client.RetryBaseDelay = time.Millisecond
		return client
	}
	d.startExecutor = func(task db.Task, run db.Run) {
		f.started = append(f.started, task.ID)
		report, finished := f.executor(task)
		if !finished {
			return
		}
		f.finishRun(run.ID, report)
		d.Enqueue(task.ID)
	}
	return d
}

func (f *driverFixture) finishRun(runID int32, report workflow.Report) {
	payload, _ := json.Marshal(report)
	require.NoError(f.t, f.database.Model(&db.Run{}).Where("id = ?", runID).
		Updates(map[string]interface{}{"status": "completed", "report": string(payload), "ended_at": time.Now()}).Error)
}

// root creates a top-level managed task of the given type, queued to start.
func (f *driverFixture) root(taskType string) db.Task {
	ceo := f.agents["CEO"].ID
	task, err := f.q.CreateTask(context.Background(), db.Task{
		CompanyID: f.company.ID, SprintID: f.sprint.ID, AgentID: &ceo, Title: "Add single sign-on",
		Description: "Customers want SSO.", TaskType: taskType, Status: db.TaskStatusTodo,
	})
	require.NoError(f.t, err)
	return task
}

// run drives a task until nothing more can happen without outside input.
func (f *driverFixture) run(taskID int32) {
	f.driver.Enqueue(taskID)
	f.driver.runUntilIdle(context.Background())
}

func (f *driverFixture) task(id int32) db.Task {
	task, err := f.q.GetTask(context.Background(), id)
	require.NoError(f.t, err)
	return task
}

func (f *driverFixture) children(parentID int32) []db.Task {
	var children []db.Task
	require.NoError(f.t, f.database.Where("parent_id = ?", parentID).Order("id").Find(&children).Error)
	return children
}

// steps returns a task's whole journal, prompts and answers included.
func (f *driverFixture) steps(taskID int32) []db.TaskStep {
	var steps []db.TaskStep
	require.NoError(f.t, f.database.Where("task_id = ?", taskID).Order("id").Find(&steps).Error)
	return steps
}

// runUntilIdle advances queued tasks on the calling goroutine until the queue
// is empty: a deterministic driver with no worker goroutines.
func (d *workflowDriver) runUntilIdle(ctx context.Context) {
	for {
		taskID, ok := d.take()
		if !ok {
			return
		}
		d.advance(ctx, taskID)
		d.finish(taskID)
	}
}

func kindsOf(steps []db.TaskStep) []string {
	kinds := make([]string, 0, len(steps))
	for _, step := range steps {
		kind := step.Kind
		if step.ToolName != "" {
			kind += ":" + step.ToolName
		}
		kinds = append(kinds, kind)
	}
	return kinds
}

func (f *driverFixture) usage(filter db.UsageFilter) db.UsageTotals {
	filter.CompanyID = f.company.ID
	totals, err := f.q.UsageTotals(context.Background(), filter)
	require.NoError(f.t, err)
	return totals
}

const (
	finishRefinementArgs = `{"spec":"Support SAML login","definition_of_done":["Okta login works"],` + oneDecisionArg + `}`
	passArgs             = `{"passed":true,"criteria":[{"criterion":"Okta login works","passed":true,"note":"seen"}],"summary":"shipped",` + oneDecisionArg + `}`
)

func TestDriverRunsAResearchTaskFromStartToReview(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeResearch)
	f.executor = func(task db.Task) (workflow.Report, bool) {
		if strings.Contains(task.Title, "SAML library") {
			return workflow.Report{Status: workflow.ReportCannotComplete, Summary: "go.mod has none and policy is unknown", Details: "searched go.mod and vendor/"}, true
		}
		return workflow.Report{Status: workflow.ReportDone, Summary: "answer for: " + task.Title, Evidence: []string{"looked"}}, true
	}
	f.provider.push(
		call(workflow.ToolAskQuestions, `{"questions":[{"question":"How is auth done today?"},{"question":"Is a SAML library vendored?"}],"decisions":[]}`),
		call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolCreateTasks, `{"tasks":[
			{"key":"a","title":"Survey providers","instructions":"list them","done_when":"listed","type":"research"},
			{"key":"b","title":"Write summary","instructions":"summarise","done_when":"written","type":"research","depends_on":["a"]}],`+oneDecisionArg+`}`),
		call(workflow.ToolFinishVerification, passArgs),
	)

	f.run(root.ID)

	done := f.task(root.ID)
	assert.Equal(t, db.TaskStatusInReview, done.Status, "a passing root stops for the human's review")
	assert.Empty(t, done.Phase)
	assert.Empty(t, done.WaitingOn)
	assert.Empty(t, done.LeaseOwner, "the lease is released when the driver is done with the task")
	assert.Equal(t, "Support SAML login", done.RefinedDescription)
	assert.Contains(t, done.AcceptanceCriteria, `"status":"passed"`)
	assert.Equal(t, "shipped", done.ResultSummary)
	assert.Equal(t, 4, done.SmartStepsUsed)

	assert.Equal(t, []string{
		"workflow_started",
		"smart_call:ask_questions", "subtask_finished",
		"smart_call:finish_refinement", "phase_entered",
		"smart_call:create_tasks", "phase_entered",
		"phase_entered",
		"smart_call:finish_verification", "finished",
	}, kindsOf(f.steps(root.ID)))

	// Each smart step saw a freshly composed prompt carrying what was known.
	require.Len(t, f.provider.served(), 4, "four smart calls for the whole task")
	for i, request := range f.provider.served() {
		assert.Len(t, request.Messages, 2, "request %d is one system and one user message: no history", i)
		assert.Equal(t, aicli.ToolChoiceRequired, request.ToolChoice)
		assert.Equal(t, "smart-model", request.Model)
	}
	assert.Contains(t, f.provider.systemPrompt(0), "You are the CEO agent.")
	assert.Contains(t, f.provider.systemPrompt(0), "Phase: refinement.")
	second := f.provider.userPrompt(1)
	assert.Contains(t, second, "answer for: How is auth done today?")
	assert.Contains(t, second, "NOT ANSWERED (could not be completed): go.mod has none and policy is unknown",
		"an unanswerable question comes back to the smart model with the reason")
	assert.Contains(t, f.provider.userPrompt(2), "Support SAML login", "the plan step sees the specification")
	assert.Contains(t, f.provider.systemPrompt(2), "Phase: planning.")
	last := f.provider.userPrompt(3)
	assert.Contains(t, last, "Survey providers")
	assert.Contains(t, last, "DONE")
	assert.Contains(t, last, "Decisions so far")

	children := f.children(root.ID)
	require.Len(t, children, 4)
	for _, child := range children[:2] {
		assert.Equal(t, models.TaskTypeResearch, child.TaskType)
		assert.Equal(t, models.TaskModeDirect, child.Mode)
		assert.Equal(t, models.TaskPhaseRefine, child.OriginPhase)
		assert.Equal(t, models.TaskPhaseRefine, child.WorkflowPhase, "a question asked during refinement is refinement's cost")
	}
	assert.Equal(t, db.TaskStatusDone, children[0].Status)
	assert.Equal(t, db.TaskStatusFailed, children[1].Status)
	assert.Equal(t, models.TaskResultCannotComplete, children[1].ResultReason)
	for _, child := range children[2:] {
		assert.Equal(t, db.TaskStatusDone, child.Status)
		assert.Equal(t, models.TaskPhasePlan, child.OriginPhase)
		assert.Equal(t, models.TaskPhaseExecute, child.WorkflowPhase, "work the plan delegates is execution's cost")
		assert.Contains(t, child.Description, "Done when:")
	}
	assert.Equal(t, []int32{children[0].ID, children[1].ID, children[2].ID, children[3].ID}, f.started,
		"the dependent task started only after the one it depends on")

	decisions, err := f.q.ListDecisionsByRoot(context.Background(), root.ID)
	require.NoError(t, err)
	require.Len(t, decisions, 3, "one decision from each deciding step")
	for _, decision := range decisions {
		require.NotNil(t, decision.StepID)
		require.NotNil(t, decision.AgentID)
		assert.Equal(t, f.agents["CEO"].ID, *decision.AgentID)
	}
	assert.Equal(t, []string{models.TaskPhaseRefine, models.TaskPhasePlan, models.TaskPhaseVerify},
		[]string{decisions[0].Phase, decisions[1].Phase, decisions[2].Phase})

	// The journal and the decisions are mirrored to the task's log folder.
	dir := taskLogDir(f.basePath, "hc1", done)
	journal, err := os.ReadFile(filepath.Join(dir, journalFileName))
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimSpace(string(journal)), "\n"), len(f.steps(root.ID)))
	mirrored, err := os.ReadFile(filepath.Join(dir, decisionsFileName))
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimSpace(string(mirrored)), "\n"), 3)
}

func TestDriverRecordsEverySmartCallInTheLedger(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeResearch)
	f.provider.push(
		scriptedReply{text: "Let me think about this."}, // no tool call: rejected, asked again
		call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolCreateTasks, `{"tasks":[{"key":"a","title":"A","instructions":"i","done_when":"d","type":"research"}],`+oneDecisionArg+`}`),
		call(workflow.ToolFinishVerification, passArgs),
	)
	f.run(root.ID)
	require.Equal(t, db.TaskStatusInReview, f.task(root.ID).Status)

	requests := len(f.provider.served())
	require.Equal(t, 4, requests)
	totals := f.usage(db.UsageFilter{TaskID: &root.ID})
	assert.Equal(t, int64(requests), totals.Calls, "one ledger row per provider round trip")
	assert.Equal(t, int64(requests*f.provider.promptTokens), totals.PromptTokens, "the ledger adds up to what the provider reported")
	assert.Equal(t, int64(requests*f.provider.completionTokens), totals.CompletionTokens)
	assert.Equal(t, int64(1), totals.FailedCalls, "the rejected answer is counted, and counted as not ok")

	calls, err := f.q.ListLLMCalls(context.Background(), db.UsageFilter{CompanyID: f.company.ID, TaskID: &root.ID}, 0, 0)
	require.NoError(t, err)
	require.Len(t, calls, 4)
	steps := map[int64]db.TaskStep{}
	for _, step := range f.steps(root.ID) {
		steps[step.ID] = step
	}
	for _, c := range calls {
		assert.Equal(t, models.TierSmart, c.Tier)
		assert.Equal(t, "smart-model", c.RequestedModel)
		assert.Equal(t, "smart-model-served", c.Model, "the model that actually answered is recorded")
		assert.Equal(t, "CEO", c.AgentName)
		assert.Equal(t, root.ID, *c.RootTaskID)
		require.NotNil(t, c.StepID, "every call opens as a log: it links to its journal step")
		step := steps[*c.StepID]
		assert.Equal(t, models.StepSmartCall, step.Kind)
		assert.Contains(t, step.Prompt, `"messages"`, "the step holds the full prompt")
		assert.NotEmpty(t, step.Response, "and the model's raw reply")
		assert.Equal(t, step.Phase, c.Phase)
		assert.Equal(t, step.Phase, c.WorkflowPhase, "a top-level task's own calls belong to the phase it was in")
	}
	// Newest first: verify, plan, then the two refinement round trips.
	assert.Equal(t, []string{models.LLMCallOK, models.LLMCallOK, models.LLMCallOK, models.LLMCallRejected},
		[]string{calls[0].Status, calls[1].Status, calls[2].Status, calls[3].Status})
	assert.Equal(t, *calls[2].StepID, *calls[3].StepID, "the rejected round trip belongs to the same step as the accepted one")

	byPhase, err := f.q.UsageBy(context.Background(), db.UsageFilter{CompanyID: f.company.ID, SubtreeOf: &root.ID}, db.UsageByPhase)
	require.NoError(t, err)
	phases := map[string]int64{}
	for _, group := range byPhase {
		phases[group.Key] = group.Calls
	}
	assert.Equal(t, map[string]int64{models.TaskPhaseRefine: 2, models.TaskPhasePlan: 1, models.TaskPhaseVerify: 1}, phases)
}

func TestDriverCodingTaskUsesRolesAndRunsTheReviewLoopItself(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeCoding)
	reviews := 0
	f.executor = func(task db.Task) (workflow.Report, bool) {
		if task.TaskType != models.TaskTypeReview {
			return workflow.Report{Status: workflow.ReportDone, Summary: "implemented " + task.Title}, true
		}
		reviews++
		if reviews == 1 {
			return workflow.Report{Status: workflow.ReportDone, Summary: "callback returns 500 without NameID", Verdict: models.TaskVerdictChangesRequested}, true
		}
		return workflow.Report{Status: workflow.ReportDone, Summary: "all findings resolved", Verdict: models.TaskVerdictApproved}, true
	}
	f.provider.push(
		call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolFinishDesign, `{"design":"Add a SAML handler",`+oneDecisionArg+`}`),
		call(workflow.ToolFinishTestPlan, `{"test_scenarios":["valid assertion logs in"],`+oneDecisionArg+`}`),
		call(workflow.ToolCreateTasks, `{"tasks":[{"key":"h","title":"SAML handler","instructions":"write it","done_when":"tests pass","type":"coding"}],`+oneDecisionArg+`}`),
		call(workflow.ToolFinishVerification, passArgs),
	)

	f.run(root.ID)

	done := f.task(root.ID)
	require.Equal(t, db.TaskStatusInReview, done.Status)
	assert.Equal(t, "Add a SAML handler", done.Design)
	assert.Contains(t, done.TestCases, "valid assertion logs in")

	// The role that speaks changes with the phase.
	require.Len(t, f.provider.served(), 5, "the review round cost no smart call")
	speakers := []string{"CEO", "CTO", "QA Lead", "CTO", "CEO"}
	for i, want := range speakers {
		assert.True(t, strings.HasPrefix(f.provider.systemPrompt(i), "You are the "+want+" agent."), "step %d should be spoken by %s", i, want)
	}
	assert.Contains(t, f.provider.userPrompt(3), "Add a SAML handler", "planning sees the design")
	assert.Contains(t, f.provider.userPrompt(3), "valid assertion logs in", "and the test scenarios")

	children := f.children(root.ID)
	require.Len(t, children, 4, "implementation, its review, then one fix and its review")
	implementation, firstReview, fix, secondReview := children[0], children[1], children[2], children[3]
	assert.Equal(t, models.TaskTypeCoding, implementation.TaskType)
	assert.Equal(t, f.agents["Coder"].ID, *implementation.AgentID, "implementation goes to the Coder by default")
	assert.Equal(t, models.TaskTypeReview, firstReview.TaskType)
	assert.Equal(t, f.agents["QA"].ID, *firstReview.AgentID)
	assert.Equal(t, models.TaskVerdictChangesRequested, firstReview.ResultVerdict)
	assert.Equal(t, db.TaskStatusDone, firstReview.Status, "a review that finds problems still finished its job")
	assert.Equal(t, models.TaskTypeCoding, fix.TaskType)
	assert.Equal(t, 1, fix.ReviewRound)
	assert.Contains(t, fix.Description, "callback returns 500 without NameID")
	assert.Equal(t, models.TaskVerdictApproved, secondReview.ResultVerdict)
	for _, child := range children {
		assert.Equal(t, models.TaskPhaseExecute, child.WorkflowPhase)
	}
	assert.Equal(t, []int32{implementation.ID, firstReview.ID, fix.ID, secondReview.ID}, f.started, "each ran after the one it follows")

	root = f.task(root.ID)
	assert.Nil(t, root.WorkspaceOwnerTaskID, "the worktree is free again once the writers are done")
	assert.Contains(t, kindsOf(f.steps(root.ID)), "review_round")
}

// Work that builds on a piece of implementation must build on the accepted
// one. If the schema's review asks for a change, the API waits for the fix and
// its review instead of being written, and approved, against the old schema.
func TestDriverHoldsDependentWorkUntilTheReviewAccepts(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeCoding)
	schemaReviews := 0
	f.executor = func(task db.Task) (workflow.Report, bool) {
		if task.TaskType != models.TaskTypeReview {
			return workflow.Report{Status: workflow.ReportDone, Summary: "implemented " + task.Title}, true
		}
		if task.Title == "Review: Schema" {
			if schemaReviews++; schemaReviews == 1 {
				return workflow.Report{Status: workflow.ReportDone, Summary: "user_id must be a UUID", Verdict: models.TaskVerdictChangesRequested}, true
			}
		}
		return workflow.Report{Status: workflow.ReportDone, Summary: "accepted", Verdict: models.TaskVerdictApproved}, true
	}
	f.provider.push(
		call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolFinishDesign, `{"design":"A users table and an API over it",`+oneDecisionArg+`}`),
		call(workflow.ToolFinishTestPlan, `{"test_scenarios":["a user can be fetched"],`+oneDecisionArg+`}`),
		call(workflow.ToolCreateTasks, `{"tasks":[
			{"key":"db","title":"Schema","instructions":"add the users table","done_when":"migrates","type":"coding"},
			{"key":"api","title":"API","instructions":"serve users","done_when":"tests pass","type":"coding","depends_on":["db"]}],`+oneDecisionArg+`}`),
		call(workflow.ToolFinishVerification, passArgs),
	)

	f.run(root.ID)

	require.Equal(t, db.TaskStatusInReview, f.task(root.ID).Status)
	require.Len(t, f.provider.served(), 5, "the fix round cost no smart call")
	byTitle := map[string][]db.Task{}
	for _, child := range f.children(root.ID) {
		byTitle[child.Title] = append(byTitle[child.Title], child)
		assert.Equal(t, db.TaskStatusDone, child.Status, child.Title)
	}
	require.Len(t, f.children(root.ID), 6, "schema, its review, the fix, its review, then the API and its review")
	schema, api := byTitle["Schema"][0], byTitle["API"][0]
	firstReview, secondReview := byTitle["Review: Schema"][0], byTitle["Review: Schema"][1]
	fix := byTitle["Address review findings (round 1): Schema"][0]

	position := map[int32]int{}
	for i, id := range f.started {
		position[id] = i
	}
	assert.Less(t, position[firstReview.ID], position[fix.ID])
	assert.Less(t, position[fix.ID], position[secondReview.ID])
	assert.Less(t, position[secondReview.ID], position[api.ID], "the API was written only after the corrected schema was accepted")

	prerequisites, err := f.q.ListPrerequisites(context.Background(), api.ID)
	require.NoError(t, err)
	ids := []int32{}
	for _, prerequisite := range prerequisites {
		ids = append(ids, prerequisite.ID)
	}
	assert.ElementsMatch(t, []int32{schema.ID, secondReview.ID}, ids, "the API now waits on the review that accepted the schema")
	fixPrerequisites, err := f.q.ListPrerequisites(context.Background(), fix.ID)
	require.NoError(t, err)
	require.Len(t, fixPrerequisites, 1)
	assert.Equal(t, firstReview.ID, fixPrerequisites[0].ID, "the fix still follows the review it answers")
}

// When the fix rounds run out, what waited for the review is canceled rather
// than left waiting, and the smart model re-plans with the whole picture.
func TestDriverCancelsDependentWorkWhenTheReviewNeverAccepts(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeCoding)
	f.executor = func(task db.Task) (workflow.Report, bool) {
		if task.Title == "Review: Schema" {
			return workflow.Report{Status: workflow.ReportDone, Summary: "still wrong", Verdict: models.TaskVerdictChangesRequested}, true
		}
		return workflow.Report{Status: workflow.ReportDone, Summary: "done: " + task.Title, Verdict: models.TaskVerdictApproved}, true
	}
	f.provider.push(
		call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolFinishDesign, `{"design":"A users table and an API over it",`+oneDecisionArg+`}`),
		call(workflow.ToolFinishTestPlan, `{"test_scenarios":["a user can be fetched"],`+oneDecisionArg+`}`),
		call(workflow.ToolCreateTasks, `{"tasks":[
			{"key":"db","title":"Schema","instructions":"add the users table","done_when":"migrates","type":"coding"},
			{"key":"api","title":"API","instructions":"serve users","done_when":"tests pass","type":"coding","depends_on":["db"]}],`+oneDecisionArg+`}`),
		call(workflow.ToolFinishAdjustment, `{"reason":"the schema is acceptable as it is",`+oneDecisionArg+`}`),
		call(workflow.ToolFinishVerification, passArgs),
	)

	f.run(root.ID)

	require.Equal(t, db.TaskStatusInReview, f.task(root.ID).Status)
	for _, child := range f.children(root.ID) {
		if child.Title != "API" && child.Title != "Review: API" {
			continue
		}
		assert.Equal(t, db.TaskStatusCanceled, child.Status, child.Title)
		assert.NotContains(t, f.started, child.ID, "nothing was built on a schema that was never accepted")
	}
	assert.Contains(t, f.provider.userPrompt(4), "still requests changes")
}

// A retried coding task is new code. The review of the failed attempt was
// canceled with it, so the new attempt gets a review of its own and the plan
// does not move on until that review has accepted it.
func TestDriverReviewsARetriedCodingTask(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeCoding)
	attempts, reviewed := 0, []string{}
	f.executor = func(task db.Task) (workflow.Report, bool) {
		if task.TaskType == models.TaskTypeReview {
			reviewed = append(reviewed, task.Description)
			return workflow.Report{Status: workflow.ReportDone, Summary: "accepted", Verdict: models.TaskVerdictApproved}, true
		}
		if attempts++; attempts == 1 {
			return workflow.Report{Status: workflow.ReportFailed, Summary: "the library has no SAML support"}, true
		}
		return workflow.Report{Status: workflow.ReportDone, Summary: "implemented " + task.Title}, true
	}
	f.provider.push(
		call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolFinishDesign, `{"design":"Add a SAML handler",`+oneDecisionArg+`}`),
		call(workflow.ToolFinishTestPlan, `{"test_scenarios":["valid assertion logs in"],`+oneDecisionArg+`}`),
		call(workflow.ToolCreateTasks, `{"tasks":[{"key":"h","title":"SAML handler","instructions":"write it","done_when":"tests pass","type":"coding"}],`+oneDecisionArg+`}`),
		// The first subtask is the implementation; it fails and is retried.
		call(workflow.ToolRetryTasks, fmt.Sprintf(`{"task_ids":[%d],"instructions":"use the other library",`+oneDecisionArg+`}`, root.ID+1)),
		call(workflow.ToolFinishVerification, passArgs),
	)

	f.run(root.ID)

	require.Equal(t, db.TaskStatusInReview, f.task(root.ID).Status)
	children := f.children(root.ID)
	require.Len(t, children, 4, "the failed attempt and its canceled review, then the new attempt and its review")
	failed := children[:2]
	require.Equal(t, db.TaskStatusFailed, failed[0].Status)
	require.Equal(t, db.TaskStatusCanceled, failed[1].Status, "the review of the failed attempt never ran")
	retry, review := children[2], children[3]
	assert.Equal(t, models.TaskTypeCoding, retry.TaskType)
	assert.Equal(t, models.TaskTypeReview, review.TaskType)
	assert.Equal(t, "Review: SAML handler", review.Title)
	assert.Equal(t, f.agents["QA"].ID, *review.AgentID)
	assert.Equal(t, db.TaskStatusDone, review.Status)
	assert.Equal(t, models.TaskVerdictApproved, review.ResultVerdict)
	require.Len(t, reviewed, 1, "exactly the new code was reviewed")
	assert.Contains(t, reviewed[0], "use the other library")
	assert.Equal(t, []int32{failed[0].ID, retry.ID, review.ID}, f.started, "verification came after the review")
}

func TestDriverAdjustsAfterAFailureUsingTheDecisionTree(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeGeneral)
	attempts := 0
	f.executor = func(task db.Task) (workflow.Report, bool) {
		if strings.Contains(task.Title, "Fetch prices") {
			attempts++
			if attempts == 1 {
				return workflow.Report{Status: workflow.ReportFailed, Summary: "the v1 endpoint is gone", Details: "GET /v1/prices returns 410"}, true
			}
		}
		return workflow.Report{Status: workflow.ReportDone, Summary: "ok: " + task.Title}, true
	}
	f.provider.push(
		call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolCreateTasks, `{"tasks":[
			{"key":"fetch","title":"Fetch prices","instructions":"call the API","done_when":"fetched","type":"research"},
			{"key":"report","title":"Write report","instructions":"use the prices","done_when":"written","type":"research","depends_on":["fetch"]}],
			"decisions":[{"title":"Use the pricing API","decision":"fetch from v1","rationale":"it is documented","alternatives":["scrape the site"]}]}`),
		call(workflow.ToolGetDecisionTree, `{"scope":"task"}`),
		call(workflow.ToolGetExecutionState, `{}`),
		call(workflow.ToolCreateTasks, `{"tasks":[
			{"key":"fetch2","title":"Fetch prices","instructions":"call the v2 API","done_when":"fetched","type":"research"},
			{"key":"report2","title":"Write report","instructions":"use the prices","done_when":"written","type":"research","depends_on":["fetch2"]}],`+oneDecisionArg+`}`),
		call(workflow.ToolFinishVerification, passArgs),
	)

	f.run(root.ID)

	done := f.task(root.ID)
	require.Equal(t, db.TaskStatusInReview, done.Status)
	assert.Equal(t, 1, done.AdjustCyclesUsed)

	children := f.children(root.ID)
	require.Len(t, children, 4)
	assert.Equal(t, db.TaskStatusFailed, children[0].Status)
	assert.Equal(t, db.TaskStatusCanceled, children[1].Status, "work that depended on the failure never started")
	assert.Equal(t, models.TaskResultPrerequisiteFailed, children[1].ResultReason)
	assert.NotContains(t, f.started, children[1].ID)
	assert.Equal(t, models.TaskPhaseAdjust, children[2].WorkflowPhase, "work created while re-planning is adjustment's cost")
	assert.Equal(t, db.TaskStatusDone, children[3].Status)

	// The first adjust prompt says why the task is there and offers the
	// read-only tools; each read comes back in the next prompt.
	adjust := f.provider.userPrompt(2)
	assert.Contains(t, adjust, "Why you are here")
	assert.Contains(t, adjust, fmt.Sprintf("task %d failed (reported_failure)", children[0].ID))
	assert.Contains(t, adjust, "the v1 endpoint is gone")
	assert.Contains(t, f.provider.systemPrompt(2), "Phase: adjustment.")
	afterTree := f.provider.userPrompt(3)
	assert.Contains(t, afterTree, "What you asked to read")
	assert.Contains(t, afterTree, "Use the pricing API: fetch from v1 — because it is documented (rejected: scrape the site)")
	afterState := f.provider.userPrompt(4)
	assert.Contains(t, afterState, "GET /v1/prices returns 410", "the full report is there after asking for it")
	assert.Contains(t, afterState, "Output of get_decision_tree", "earlier reads in this phase are kept")

	// Verification sees the old failures as dealt with.
	assert.Contains(t, f.provider.userPrompt(5), "already dealt with by an earlier re-plan")
}

func TestDriverAsksTheHumanAndWaitsAcrossARestart(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeResearch)
	f.provider.push(call(workflow.ToolAskHuman, `{"question":"Which identity providers matter first?","why":"it sets the scope","decisions":[]}`))
	f.run(root.ID)

	waiting := f.task(root.ID)
	assert.Equal(t, db.TaskStatusBlocked, waiting.Status, "a root shows as blocked while it waits for the human")
	assert.Equal(t, models.TaskWaitHuman, waiting.WaitingOn)
	require.NotNil(t, waiting.WaitRef)
	var question db.Comment
	require.NoError(t, f.database.First(&question, *waiting.WaitRef).Error)
	assert.Equal(t, "ask_user", question.CommentType)
	assert.Equal(t, "Which identity providers matter first?", question.Content)
	assert.Equal(t, root.ID, question.TaskID)

	// A restart: a brand-new driver finds the task and correctly does nothing.
	f.driver = f.newDriver()
	f.driver.sweep(context.Background())
	f.driver.runUntilIdle(context.Background())
	assert.Equal(t, models.TaskWaitHuman, f.task(root.ID).WaitingOn)
	require.Len(t, f.provider.served(), 1, "no model call is made while waiting")

	// Things that are not an answer do not resume it.
	require.NoError(t, f.database.Create(&db.Comment{TaskID: root.ID, AuthorType: "human", CommentType: "status_change", Content: `{"from":"a","to":"b"}`}).Error)
	require.NoError(t, f.database.Create(&db.Comment{TaskID: root.ID, AuthorType: "agent", Content: "a note"}).Error)
	f.driver.sweep(context.Background())
	f.driver.runUntilIdle(context.Background())
	assert.Equal(t, models.TaskWaitHuman, f.task(root.ID).WaitingOn)

	answer := db.Comment{TaskID: root.ID, AuthorType: "human", Content: "Okta, then Azure AD.", ReplyToID: &question.ID}
	require.NoError(t, f.database.Create(&answer).Error)
	f.provider.push(call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolAskHuman, `{"question":"May I add a dependency?","why":"policy","decisions":[]}`))
	f.driver.sweep(context.Background())
	f.driver.runUntilIdle(context.Background())

	assert.Contains(t, f.provider.userPrompt(1), "You asked: Which identity providers matter first?\nThey answered: Okta, then Azure AD.")
	resumed := f.task(root.ID)
	assert.Equal(t, models.TaskPhasePlan, resumed.Phase, "it continued through refinement into planning")
	assert.Equal(t, db.TaskStatusBlocked, resumed.Status, "and is now waiting on its second question")
	require.NotEqual(t, *waiting.WaitRef, *resumed.WaitRef)

	// A plain comment answers the oldest open question, once.
	plain := db.Comment{TaskID: root.ID, AuthorType: "human", Content: "Yes, go ahead."}
	require.NoError(t, f.database.Create(&plain).Error)
	f.provider.push(call(workflow.ToolCreateTasks, `{"tasks":[{"key":"a","title":"A","instructions":"i","done_when":"d","type":"research"}],`+oneDecisionArg+`}`),
		call(workflow.ToolFinishVerification, passArgs))
	f.driver.sweep(context.Background())
	f.driver.runUntilIdle(context.Background())
	assert.Contains(t, f.provider.userPrompt(3), "They answered: Yes, go ahead.")
	assert.Equal(t, db.TaskStatusInReview, f.task(root.ID).Status)

	var answers []db.TaskStep
	for _, step := range f.steps(root.ID) {
		if step.Kind == models.StepHumanAnswer {
			answers = append(answers, step)
		}
	}
	require.Len(t, answers, 2)
	assert.Equal(t, answer.ID, *answers[0].CommentID)
	assert.Equal(t, plain.ID, *answers[1].CommentID, "the journal records which comment answered")
}

func TestDriverBacksOffThenEscalatesWhenTheModelKeepsFailing(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeResearch)
	now := time.Now()
	f.driver.now = func() time.Time { return now }
	f.provider.push(scriptedReply{status: 500}, scriptedReply{status: 500}, scriptedReply{status: 500})

	f.run(root.ID)
	first := f.task(root.ID)
	assert.Equal(t, db.TaskStatusInProgress, first.Status)
	assert.Equal(t, models.TaskWaitBackoff, first.WaitingOn)
	assert.Equal(t, 1, first.AttemptsUsed)
	assert.Equal(t, 0, first.SmartStepsUsed, "a call that produced nothing is not a step")
	require.NotNil(t, first.WaitUntil)
	require.Len(t, f.provider.served(), 1)

	// Before the wait is over, looking at the task changes nothing.
	f.run(root.ID)
	require.Len(t, f.provider.served(), 1)

	now = now.Add(time.Minute)
	f.run(root.ID)
	require.Len(t, f.provider.served(), 2)
	assert.Equal(t, 2, f.task(root.ID).AttemptsUsed)

	// The third failure in a row stops the retrying and asks the human.
	now = now.Add(10 * time.Minute)
	f.run(root.ID)
	require.Len(t, f.provider.served(), 3)
	escalated := f.task(root.ID)
	assert.Equal(t, db.TaskStatusBlocked, escalated.Status)
	assert.Equal(t, models.TaskWaitHuman, escalated.WaitingOn)
	assert.Equal(t, models.TaskPhaseAdjust, escalated.Phase)
	var question db.Comment
	require.NoError(t, f.database.First(&question, *escalated.WaitRef).Error)
	assert.Contains(t, question.Content, "failed 3 times in a row")

	totals := f.usage(db.UsageFilter{TaskID: &root.ID})
	assert.Equal(t, int64(3), totals.Calls)
	assert.Equal(t, int64(3), totals.FailedCalls, "failed calls are in the ledger too")
	assert.Equal(t, []string{"workflow_started", "smart_error", "smart_error", "smart_error", "human_question"}, kindsOf(f.steps(root.ID)))
}

func TestDriverWaitsForALockedVaultAndAMissingModel(t *testing.T) {
	t.Run("locked vault", func(t *testing.T) {
		f := newDriverFixture(t)
		root := f.root(models.TaskTypeResearch)
		secrets.Default().LockUser(f.owner.ID)
		f.run(root.ID)

		waiting := f.task(root.ID)
		assert.Equal(t, db.TaskStatusInProgress, waiting.Status)
		assert.Equal(t, models.TaskWaitVault, waiting.WaitingOn)
		assert.Contains(t, waiting.WaitDetail, "sign in")
		assert.Zero(t, waiting.AttemptsUsed, "not the task's fault, so not counted against it")
		assert.Empty(t, f.provider.served(), "no call is attempted with a sealed key")

		f.run(root.ID)
		assert.Equal(t, models.TaskWaitVault, f.task(root.ID).WaitingOn, "still locked: still waiting")

		f.unlock()
		f.provider.push(call(workflow.ToolAskHuman, `{"question":"q","why":"w","decisions":[]}`))
		f.driver.sweep(context.Background())
		f.driver.runUntilIdle(context.Background())
		assert.Equal(t, models.TaskWaitHuman, f.task(root.ID).WaitingOn, "it continued as soon as the vault was unlocked")
	})
	t.Run("no model configured", func(t *testing.T) {
		f := newDriverFixture(t)
		_, err := f.q.UpdateDefaultModelSetting(context.Background(), f.owner.ID, db.PurposeSmart, nil, "", nil)
		require.NoError(t, err)
		root := f.root(models.TaskTypeResearch)
		f.run(root.ID)

		waiting := f.task(root.ID)
		assert.Equal(t, models.TaskWaitConfig, waiting.WaitingOn)
		assert.Contains(t, waiting.WaitDetail, "no smart model is configured")

		f.setTier(db.PurposeSmart, "smart-model")
		f.provider.push(call(workflow.ToolAskHuman, `{"question":"q","why":"w","decisions":[]}`))
		f.driver.sweep(context.Background())
		f.driver.runUntilIdle(context.Background())
		assert.Equal(t, models.TaskWaitHuman, f.task(root.ID).WaitingOn)
	})
	t.Run("a per-task model overrides the tier default", func(t *testing.T) {
		f := newDriverFixture(t)
		root := f.root(models.TaskTypeResearch)
		require.NoError(t, f.database.Model(&db.Task{}).Where("id = ?", root.ID).
			Updates(map[string]interface{}{"provider_id": f.llm.ID, "model": "special-model"}).Error)
		f.provider.pushFor("special-model", call(workflow.ToolAskHuman, `{"question":"q","why":"w","decisions":[]}`))
		f.run(root.ID)
		require.Len(t, f.provider.served(), 1)
		assert.Equal(t, "special-model", f.provider.served()[0].Model)
	})
}

func TestDriverRecoversFromACrashAtEveryPoint(t *testing.T) {
	planArgs := `{"tasks":[{"key":"a","title":"A","instructions":"i","done_when":"d","type":"research"}],` + oneDecisionArg + `}`

	t.Run("during a smart call: the step is redone", func(t *testing.T) {
		f := newDriverFixture(t)
		root := f.root(models.TaskTypeResearch)
		// The first driver dies mid-call: its request never returns and its
		// context is torn down without anything being committed.
		f.provider.hold = make(chan struct{})
		f.provider.push(call(workflow.ToolFinishRefinement, finishRefinementArgs))
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for len(f.provider.served()) == 0 {
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
		}()
		f.driver.Enqueue(root.ID)
		f.driver.runUntilIdle(ctx)

		crashed := f.task(root.ID)
		assert.Equal(t, models.TaskPhaseRefine, crashed.Phase, "nothing of the interrupted step was committed")
		assert.Zero(t, crashed.SmartStepsUsed)
		assert.Equal(t, []string{"workflow_started"}, kindsOf(f.steps(root.ID)))

		// A dead process would also leave its lease behind. Until it expires
		// nobody else touches the task; once it has, the next driver does.
		require.NoError(t, f.database.Model(&db.Task{}).Where("id = ?", root.ID).
			UpdateColumns(map[string]interface{}{"lease_owner": "dead-driver", "lease_until": time.Now().Add(time.Hour)}).Error)
		f.provider.hold = nil
		f.driver = f.newDriver()
		f.driver.sweep(context.Background())
		f.driver.runUntilIdle(context.Background())
		assert.Len(t, f.provider.served(), 1, "a live lease is respected")

		require.NoError(t, f.database.Model(&db.Task{}).Where("id = ?", root.ID).
			UpdateColumn("lease_until", time.Now().Add(-time.Second)).Error)
		f.provider.push(call(workflow.ToolFinishRefinement, finishRefinementArgs), call(workflow.ToolCreateTasks, planArgs), call(workflow.ToolFinishVerification, passArgs))
		f.driver.sweep(context.Background())
		f.driver.runUntilIdle(context.Background())
		assert.Equal(t, db.TaskStatusInReview, f.task(root.ID).Status, "the task ran to the end after the restart")
	})

	t.Run("after a step committed but before anyone was woken: the sweeper finds the subtasks", func(t *testing.T) {
		f := newDriverFixture(t)
		root := f.root(models.TaskTypeResearch)
		f.provider.push(call(workflow.ToolFinishRefinement, finishRefinementArgs), call(workflow.ToolCreateTasks, planArgs), call(workflow.ToolFinishVerification, passArgs))
		// Reach the point where the plan is about to be applied, then apply it
		// without any of what normally follows a commit.
		ctx := context.Background()
		owner := "about-to-crash"
		for {
			held, err := f.q.AcquireTaskLease(ctx, root.ID, owner, time.Minute)
			require.NoError(t, err)
			require.True(t, held)
			loaded, err := f.driver.load(ctx, root.ID)
			require.NoError(t, err)
			next := workflow.Evaluate(loaded.snapshot, f.driver.budgets)
			transition, extras := next.Transition, applyExtras{}
			if next.Kind == workflow.SmartStep {
				transition, extras, err = f.driver.smartStep(ctx, loaded, next, owner)
				require.NoError(t, err)
			}
			committed, err := f.driver.apply(ctx, loaded, transition, extras, owner)
			require.NoError(t, err)
			if len(committed.newTasks) > 0 {
				break // crash here: children exist, nobody was told
			}
			require.NoError(t, f.q.ReleaseTaskLease(ctx, root.ID, owner))
		}
		require.NoError(t, f.database.Model(&db.Task{}).Where("id = ?", root.ID).
			UpdateColumn("lease_until", time.Now().Add(-time.Second)).Error)
		children := f.children(root.ID)
		require.Len(t, children, 1)
		require.Equal(t, db.TaskStatusTodo, children[0].Status)
		require.Empty(t, f.started)

		f.driver = f.newDriver()
		f.driver.sweep(ctx)
		f.driver.runUntilIdle(ctx)
		assert.Equal(t, db.TaskStatusInReview, f.task(root.ID).Status)
		assert.Equal(t, db.TaskStatusDone, f.task(children[0].ID).Status)
	})

	t.Run("a subtask finished but its parent was never woken: the sweeper wakes it", func(t *testing.T) {
		f := newDriverFixture(t)
		root := f.root(models.TaskTypeResearch)
		// The executor does not finish on its own in this test.
		f.executor = func(db.Task) (workflow.Report, bool) { return workflow.Report{}, false }
		f.provider.push(call(workflow.ToolFinishRefinement, finishRefinementArgs), call(workflow.ToolCreateTasks, planArgs))
		f.run(root.ID)
		child := f.children(root.ID)[0]
		require.Equal(t, models.TaskWaitRun, f.task(child.ID).WaitingOn)
		require.Equal(t, models.TaskWaitSubtasks, f.task(root.ID).WaitingOn)

		// The session ends and the process dies before the child's own task
		// row is updated, let alone the parent told.
		f.finishRun(*f.task(child.ID).RunID, workflow.Report{Status: workflow.ReportDone, Summary: "found it"})
		f.driver = f.newDriver()
		f.provider.push(call(workflow.ToolFinishVerification, passArgs))
		f.driver.sweep(context.Background())
		f.driver.runUntilIdle(context.Background())

		finished := f.task(child.ID)
		assert.Equal(t, db.TaskStatusDone, finished.Status, "the stored report was applied after the restart")
		assert.Equal(t, "found it", finished.ResultSummary)
		assert.Nil(t, finished.RunID)
		assert.Equal(t, db.TaskStatusInReview, f.task(root.ID).Status)
	})

	t.Run("an executor session that died is retried, then the task fails up", func(t *testing.T) {
		f := newDriverFixture(t)
		root := f.root(models.TaskTypeResearch)
		f.executor = func(db.Task) (workflow.Report, bool) { return workflow.Report{}, false }
		f.provider.push(call(workflow.ToolFinishRefinement, finishRefinementArgs), call(workflow.ToolCreateTasks, planArgs))
		f.run(root.ID)
		child := f.children(root.ID)[0]
		firstRun := *f.task(child.ID).RunID

		fail := func(runID int32) {
			require.NoError(t, f.q.UpdateRunLog(context.Background(), runID, "session exited without a terminal status", "failed"))
		}
		fail(firstRun)
		f.run(child.ID)
		retried := f.task(child.ID)
		assert.Equal(t, db.TaskStatusInProgress, retried.Status)
		assert.Equal(t, 2, retried.AttemptsUsed)
		require.NotNil(t, retried.RunID)
		require.NotEqual(t, firstRun, *retried.RunID, "a fresh session was started")
		var second db.Run
		require.NoError(t, f.database.First(&second, *retried.RunID).Error)
		assert.Equal(t, 2, second.Attempt)

		// The second death exhausts the attempts: the subtask fails, and its
		// parent re-plans with that knowledge.
		fail(second.ID)
		f.provider.push(call(workflow.ToolEscalate, `{"reason":"the executor cannot run here",`+oneDecisionArg+`}`))
		f.run(child.ID)
		failed := f.task(child.ID)
		assert.Equal(t, db.TaskStatusFailed, failed.Status)
		assert.Equal(t, models.TaskResultRunError, failed.ResultReason)
		assert.Contains(t, failed.ResultSummary, "session exited without a terminal status")
		parent := f.task(root.ID)
		assert.Equal(t, models.TaskPhaseAdjust, parent.Phase)
		assert.Equal(t, db.TaskStatusBlocked, parent.Status, "the smart model gave up and the root asked the human")
		assert.Contains(t, f.provider.systemPrompt(2), "Phase: adjustment.")
	})
}

func TestDriverLeavesATaskAnotherDriverHolds(t *testing.T) {
	f := newDriverFixture(t)
	root := f.root(models.TaskTypeResearch)
	held, err := f.q.AcquireTaskLease(context.Background(), root.ID, "another-driver", time.Minute)
	require.NoError(t, err)
	require.True(t, held)

	f.run(root.ID)
	assert.Equal(t, db.TaskStatusTodo, f.task(root.ID).Status, "nothing happened to a task somebody else is working on")
	assert.Empty(t, f.provider.served())
	assert.Equal(t, "another-driver", f.task(root.ID).LeaseOwner, "and their lease was not disturbed")
}

func TestDriverQueueCoalescesAndRequeues(t *testing.T) {
	d := newWorkflowDriver(nil, nil)
	d.Enqueue(1)
	d.Enqueue(1)
	d.Enqueue(2)
	assert.Equal(t, []int32{1, 2}, d.queue, "asking twice is asking once")

	id, ok := d.take()
	require.True(t, ok)
	require.Equal(t, int32(1), id)
	d.Enqueue(1) // asked for again while it is being advanced
	assert.Equal(t, []int32{2}, d.queue, "it is not queued a second time while running")
	d.finish(1)
	assert.Equal(t, []int32{2, 1}, d.queue, "but it is looked at again afterwards, since it may have changed")

	id, _ = d.take()
	d.finish(id)
	id, _ = d.take()
	d.finish(id)
	_, ok = d.take()
	assert.False(t, ok)
	assert.Empty(t, d.pending, "no state is kept for tasks nobody is asking about")
}

// A smart step is not a run, so it reaches a model group through the gateway
// with a company token. This goes through the real gateway: authentication,
// tenant check, routing and the group's failover.
func TestDriverSmartStepGoesThroughAModelGroup(t *testing.T) {
	f := newDriverFixture(t)
	gateway := integration.NewLLMGateway(f.database)
	gateway.SetRunTokenValidator(runtokens.Default().Validate)
	gateway.SetCompanyTokenValidator(runtokens.Default().ValidateCompany)
	router := chi.NewRouter()
	router.Route("/api", func(r chi.Router) { gateway.Mount(r) })
	server := httptest.NewServer(router)
	defer server.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)
	t.Setenv("PORT", port)

	group := db.ModelGroup{Name: "Smart group", Slug: fmt.Sprintf("smart-group-%d", time.Now().UnixNano()), UserID: &f.owner.ID}
	require.NoError(t, f.database.Create(&group).Error)
	require.NoError(t, f.database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: f.llm.ID, Model: "group-member-model"}).Error)
	_, err = f.q.UpdateDefaultModelSetting(context.Background(), f.owner.ID, db.PurposeSmart, nil, "", &group.ID)
	require.NoError(t, err)

	root := f.root(models.TaskTypeResearch)
	// The gateway asks the provider for the group member's model.
	f.provider.pushFor("group-member-model", call(workflow.ToolAskHuman, `{"question":"q","why":"w","decisions":[]}`))
	f.run(root.ID)

	require.Equal(t, models.TaskWaitHuman, f.task(root.ID).WaitingOn, "the step was answered through the group")
	require.Len(t, f.provider.served(), 1)
	assert.Equal(t, "group-member-model", f.provider.served()[0].Model, "the gateway chose the group's member")
	assert.Equal(t, aicli.ToolChoiceRequired, f.provider.served()[0].ToolChoice, "tool_choice passes through the gateway")

	calls, err := f.q.ListLLMCalls(context.Background(), db.UsageFilter{CompanyID: f.company.ID, TaskID: &root.ID}, 0, 0)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, group.Slug, calls[0].RequestedModel, "the ledger records what was asked for")
	assert.Equal(t, "group-member-model-served", calls[0].Model, "and the model that answered")

	// The owner signs out: the group reports its keys sealed, and the task
	// waits for an unlock instead of failing or burning retries.
	second := f.root(models.TaskTypeResearch)
	secrets.Default().LockUser(f.owner.ID)
	f.run(second.ID)
	waiting := f.task(second.ID)
	assert.Equal(t, models.TaskWaitVault, waiting.WaitingOn)
	assert.Zero(t, waiting.AttemptsUsed)
	f.unlock()
}
