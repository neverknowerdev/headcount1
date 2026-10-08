package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/workflow"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/filesystem"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// executorFixture runs the real executor: the engine, its agent loop, the
// file tools and the workspaces, against the scripted provider.
type executorFixture struct {
	*driverFixture
	engine *NativeEngine
}

func newExecutorFixture(t *testing.T) *executorFixture {
	t.Helper()
	t.Setenv("E2E_HEADCOUNT1_HOME", t.TempDir())
	x := &executorFixture{driverFixture: newDriverFixture(t)}
	x.restart()
	return x
}

// restart replaces the engine with a fresh one on the same database, as a
// restarted server would.
func (x *executorFixture) restart() {
	x.engine = NewNativeEngine(x.database, eventhub.NewHub())
	x.engine.driver.newClient = func(baseURL, apiKey, model string) *aicli.Client {
		client := aicli.NewClient(baseURL, apiKey, model)
		client.MaxRetries = 0
		client.RetryBaseDelay = time.Millisecond
		return client
	}
	x.driver = x.engine.driver
}

// settle drives the workflow until nothing is queued and no executor runs.
func (x *executorFixture) settle() {
	for i := 0; i < 500; i++ {
		x.driver.runUntilIdle(context.Background())
		x.engine.runs.active.Wait()
		x.driver.mu.Lock()
		idle := len(x.driver.queue) == 0
		x.driver.mu.Unlock()
		if idle && x.engine.executorSlots.Load() == 0 {
			return
		}
	}
	x.t.Fatal("the workflow did not settle")
}

func (x *executorFixture) start(taskID int32) {
	x.driver.Enqueue(taskID)
	x.settle()
}

// direct creates a top-level direct task: one executor session, no phases.
func (x *executorFixture) direct(taskType, title, instructions string) db.Task {
	coder := x.agents["Coder"].ID
	task, err := x.q.CreateTask(context.Background(), db.Task{
		CompanyID: x.company.ID, SprintID: x.sprint.ID, AgentID: &coder, Title: title, Description: instructions,
		TaskType: taskType, Mode: models.TaskModeDirect, Status: db.TaskStatusTodo,
	})
	require.NoError(x.t, err)
	return task
}

func (x *executorFixture) paths() filesystem.Paths {
	return filesystem.NewPaths(loadSettings().BasePath)
}

func (x *executorFixture) runsOf(taskID int32) []db.Run {
	var runs []db.Run
	require.NoError(x.t, x.database.Where("task_id = ?", taskID).Order("id").Find(&runs).Error)
	return runs
}

func toolsOffered(request aicli.ChatRequest) []string {
	names := make([]string, 0, len(request.Tools))
	for _, tool := range request.Tools {
		names = append(names, tool.Function.Name)
	}
	return names
}

func lastUserMessage(request aicli.ChatRequest) string {
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == "user" {
			return request.Messages[i].Content
		}
	}
	return ""
}

func finish(status, summary string) scriptedReply {
	args, _ := json.Marshal(map[string]any{"status": status, "summary": summary})
	return call("finish_work", string(args))
}

func TestExecutorRunsADirectTaskAndReports(t *testing.T) {
	x := newExecutorFixture(t)
	task := x.direct(models.TaskTypeResearch, "Find the auth entry point", "Locate where login is handled and report the file.")
	x.provider.pushCheap(
		call("write", `{"path":"notes.txt","content":"login is in auth.go"}`),
		call("finish_work", `{"status":"done","summary":"Login is handled in auth.go","details":"See handler Login.","evidence":["read auth.go"],"open_questions":["is SSO planned?"]}`),
	)

	x.start(task.ID)

	done := x.task(task.ID)
	assert.Equal(t, db.TaskStatusDone, done.Status)
	assert.Equal(t, "Login is handled in auth.go", done.ResultSummary)
	assert.Contains(t, done.ResultDetails, "See handler Login.")
	assert.Contains(t, done.ResultDetails, "is SSO planned?")
	assert.JSONEq(t, `["read auth.go"]`, done.ResultEvidence)
	assert.Nil(t, done.RunID)
	assert.Empty(t, done.WaitingOn)

	runs := x.runsOf(task.ID)
	require.Len(t, runs, 1)
	run := runs[0]
	assert.Equal(t, "completed", run.Status)
	assert.Equal(t, 1, run.Attempt)
	assert.Equal(t, x.agents["Coder"].ID, run.AgentID)
	assert.NotNil(t, run.EndedAt)
	assert.Contains(t, run.Report, `"status":"done"`, "the report is stored on the session")

	// A reader works in a scratch directory of its own, not in the worktree.
	scratch := x.paths().RunWorkspaceDir("hc1", task.ID, run.ID)
	assert.Equal(t, scratch, run.WorkspacePath)
	written, err := os.ReadFile(filepath.Join(scratch, "notes.txt"))
	require.NoError(t, err)
	assert.Equal(t, "login is in auth.go", string(written))

	// Its log sits in the task's own folder, next to the journal.
	assert.Equal(t, filepath.Join(x.paths().TaskJournalDir("hc1", task.ID, task.ID), "run-"+idText(run.ID)+".jsonl"), run.LogFilePath)
	_, err = os.Stat(run.LogFilePath)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(filepath.Dir(run.LogFilePath), journalFileName))
	require.NoError(t, err, "the journal is in the same folder")

	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 2)
	system := requests[0].Messages[0].Content
	assert.True(t, strings.HasPrefix(system, "You are the Coder agent."))
	assert.Contains(t, system, "You cannot ask anyone a question.")
	assert.Contains(t, system, "This is a research task")
	assert.Contains(t, system, "Working directory: "+scratch)
	assert.Contains(t, requests[0].Messages[1].Content, "Locate where login is handled")
	offered := toolsOffered(requests[0])
	for _, expected := range []string{"read", "write", "ls", "grep", "bash", "web_fetch", "checkpoint", "finish_work", "write_artifact"} {
		assert.Contains(t, offered, expected)
	}
	for _, forbidden := range []string{"ask_human", "ask_questions", "create_tasks", "retry_tasks", "escalate"} {
		assert.NotContains(t, offered, forbidden, "an executor cannot ask the human or create tasks")
	}
	assert.Empty(t, requests[0].ToolChoice, "an ordinary turn is unrestricted")

	// One ledger row per turn, each pointing into the session's log.
	calls, err := x.q.ListLLMCalls(context.Background(), db.UsageFilter{CompanyID: x.company.ID, TaskID: &task.ID}, 0, 0)
	require.NoError(t, err)
	require.Len(t, calls, 2)
	log, err := os.ReadFile(run.LogFilePath)
	require.NoError(t, err)
	for _, c := range calls {
		assert.Equal(t, models.TierCheap, c.Tier)
		assert.Equal(t, "executor_turn", c.Purpose)
		assert.Equal(t, cheapModel, c.RequestedModel)
		assert.Equal(t, cheapModel+"-served", c.Model)
		assert.Equal(t, "Coder", c.AgentName)
		assert.Equal(t, models.TaskPhaseExecute, c.Phase)
		require.NotNil(t, c.RunID)
		assert.Equal(t, run.ID, *c.RunID)
		require.NotNil(t, c.LogSeq, "an executor call opens as a log: it names its place in the session log")
		assert.Contains(t, string(log), `"seq":`+strconv.FormatInt(*c.LogSeq, 10)+`,`)
		assert.Nil(t, c.StepID)
	}
	totals := x.usage(db.UsageFilter{TaskID: &task.ID})
	assert.Equal(t, int64(2*x.provider.promptTokens), totals.PromptTokens)

	assert.Equal(t, []string{"workflow_started", "run_started", "run_finished", "finished"}, kindsOf(x.steps(task.ID)))
}

func TestExecutorIsMadeToCheckpointAndRecordsEachThingOnce(t *testing.T) {
	x := newExecutorFixture(t)
	x.engine.driver.budgets.CheckpointEveryToolCalls = 3
	task := x.direct(models.TaskTypeResearch, "Survey the code", "Look around and report.")
	ls := call("ls", `{"path":"."}`)
	x.provider.pushCheap(
		ls, ls, ls,
		// Demanded: the model never volunteers a checkpoint.
		call("checkpoint", `{"progress":"Listed the top level","next_step":"read the handlers",
			"decisions":[{"title":"Start from handlers","detail":"Read the HTTP handlers first","reason":"they name the entry points"}],
			"dead_ends":[{"title":"Search by name","detail":"Grepped for 'login'","reason":"too many matches to be useful"}]}`),
		ls, ls, ls,
		// Demanded again: it restates the first decision, revises it, and adds one.
		call("checkpoint", `{"progress":"Read the handlers","next_step":"write up",
			"decisions":[
				{"title":"Start from handlers","detail":"Read the HTTP handlers first","reason":"they name the entry points"},
				{"title":"Start from the router","detail":"Read the router before the handlers","reason":"it lists every route","revises":1},
				{"title":"Ignore tests","detail":"Skip the test files","reason":"not asked for"}],
			"assumptions":[{"title":"One service","detail":"There is a single HTTP service","reason":"only one main package"}]}`),
		// The report repeats a dead end already on record and adds a new one.
		call("finish_work", `{"status":"done","summary":"Surveyed.",
			"dead_ends":[
				{"title":"search by name","detail":"grepped  for 'login'","reason":"noise"},
				{"title":"Read the docs","detail":"Looked for architecture docs","reason":"there are none"}]}`),
	)

	x.start(task.ID)
	require.Equal(t, db.TaskStatusDone, x.task(task.ID).Status)

	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 9)
	for _, demanded := range []int{3, 7} {
		assert.Equal(t, []string{"checkpoint"}, toolsOffered(requests[demanded]), "request %d offers only checkpoint", demanded)
		assert.Equal(t, aicli.ToolChoiceRequired, requests[demanded].ToolChoice)
		assert.Contains(t, lastUserMessage(requests[demanded]), "Checkpoint required")
	}
	assert.Greater(t, len(toolsOffered(requests[4])), 5, "the full tool set is back after the checkpoint")
	assert.NotContains(t, lastUserMessage(requests[3]), "Already on record", "nothing is on record at the first checkpoint")
	second := lastUserMessage(requests[7])
	assert.Contains(t, second, "Already on record for this task")
	assert.Contains(t, second, "#1 [decision] Start from handlers")
	assert.Contains(t, second, "#2 [dead_end] Search by name")

	decisions, err := x.q.ListDecisionsByTask(context.Background(), task.ID)
	require.NoError(t, err)
	type record struct{ kind, title string }
	var got []record
	for _, decision := range decisions {
		got = append(got, record{decision.Kind, decision.Title})
		require.NotNil(t, decision.RunID)
		require.NotNil(t, decision.LogSeq, "each record points at where in the session it was made")
		assert.Equal(t, models.TaskPhaseExecute, decision.Phase)
	}
	assert.Equal(t, []record{
		{models.DecisionKindDecision, "Start from handlers"},
		{models.DecisionKindDeadEnd, "Search by name"},
		{models.DecisionKindDecision, "Start from the router"},
		{models.DecisionKindDecision, "Ignore tests"},
		{models.DecisionKindAssumption, "One service"},
		{models.DecisionKindDeadEnd, "Read the docs"},
	}, got, "each thing is recorded once, however often the model restates it")
	require.NotNil(t, decisions[2].SupersedesID)
	assert.Equal(t, decisions[0].ID, *decisions[2].SupersedesID, "a revision replaces the record it names instead of standing beside it")

	// Duplicates do not vanish: the journal says what was skipped and why.
	var checkpoints, duplicates []string
	for _, step := range x.steps(task.ID) {
		switch step.Kind {
		case models.StepCheckpoint:
			checkpoints = append(checkpoints, step.Result)
		case models.StepNote:
			duplicates = append(duplicates, step.Result)
		}
	}
	assert.Equal(t, []string{"Listed the top level Next: read the handlers", "Read the handlers Next: write up"}, checkpoints)
	assert.Equal(t, []string{"duplicate of #1: Start from handlers", "duplicate of #2: search by name"}, duplicates)

	// The model was told what happened to its records.
	var toolResults []string
	for _, message := range requests[8].Messages {
		if message.Role == "tool" && strings.Contains(message.Content, "Checkpoint recorded") {
			toolResults = append(toolResults, message.Content)
		}
	}
	require.Len(t, toolResults, 2)
	assert.Contains(t, toolResults[1], `Already on record, not added again: "Start from handlers" is #1`)

	mirrored, err := os.ReadFile(filepath.Join(taskLogDir(loadSettings().BasePath, "hc1", x.task(task.ID)), decisionsFileName))
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimSpace(string(mirrored)), "\n"), 6, "the decisions file has each record once")
}

func TestExecutorThatIgnoresACheckpointDemandCarriesOn(t *testing.T) {
	x := newExecutorFixture(t)
	x.engine.driver.budgets.CheckpointEveryToolCalls = 2
	task := x.direct(models.TaskTypeResearch, "Look", "Look and report.")
	ls := call("ls", `{"path":"."}`)
	x.provider.pushCheap(ls, ls,
		scriptedReply{text: "I will checkpoint later."},
		scriptedReply{text: "Still no."},
		ls,
		finish("done", "Looked."))

	x.start(task.ID)

	require.Equal(t, db.TaskStatusDone, x.task(task.ID).Status, "a model that will not checkpoint still gets to finish its task")
	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 6)
	assert.Equal(t, []string{"checkpoint"}, toolsOffered(requests[2]))
	assert.Equal(t, []string{"checkpoint"}, toolsOffered(requests[3]), "the demand is repeated once")
	assert.Contains(t, lastUserMessage(requests[3]), "must call the `checkpoint` tool now")
	assert.Greater(t, len(toolsOffered(requests[4])), 5, "then dropped until the next interval")
}

func TestExecutorThatNeverReportsIsRetriedWithWhatItLeftBehind(t *testing.T) {
	x := newExecutorFixture(t)
	task := x.direct(models.TaskTypeResearch, "Find the bug", "Find it.")
	x.provider.pushCheap(
		call("checkpoint", `{"progress":"Reproduced the crash","next_step":"bisect",
			"dead_ends":[{"title":"Blame the cache","detail":"Disabled the cache","reason":"it still crashes"}]}`),
		// Then it just talks, three times: two reminders, and it is given up on.
		scriptedReply{text: "I think it is the parser."},
		scriptedReply{text: "Probably the parser."},
		scriptedReply{text: "Yes, the parser."},
		// The second attempt.
		finish("done", "It was the parser."),
	)

	x.start(task.ID)

	done := x.task(task.ID)
	assert.Equal(t, db.TaskStatusDone, done.Status)
	assert.Equal(t, 2, done.AttemptsUsed)
	runs := x.runsOf(task.ID)
	require.Len(t, runs, 2)
	assert.Equal(t, "failed", runs[0].Status)
	assert.Equal(t, sessionEndedSilently, runs[0].LogContent)
	assert.Equal(t, "completed", runs[1].Status)
	assert.Equal(t, 2, runs[1].Attempt)

	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 5)
	assert.Contains(t, lastUserMessage(requests[2]), "not finished until you call `finish_work`", "it is told what is expected in the same conversation")
	assert.Greater(t, len(requests[3].Messages), len(requests[2].Messages), "the reminder is part of the conversation, which keeps its history")

	retry := requests[4].Messages[1].Content
	assert.Contains(t, retry, "This is attempt 2")
	assert.Contains(t, retry, "Reproduced the crash Next: bisect", "where the earlier attempt got to")
	assert.Contains(t, retry, "[dead_end] Blame the cache: Disabled the cache", "and what it already ruled out")
	assert.Len(t, requests[4].Messages, 2, "the retry is a fresh session, not a continuation")
}

func TestExecutorWritersShareOneWorktreeOneAtATime(t *testing.T) {
	x := newExecutorFixture(t)
	root := x.root(models.TaskTypeGeneral)
	x.provider.push(
		call(workflow.ToolFinishRefinement, finishRefinementArgs),
		call(workflow.ToolCreateTasks, `{"tasks":[
			{"key":"a","title":"Write A","instructions":"write a.txt","done_when":"written","type":"general"},
			{"key":"b","title":"Write B","instructions":"write b.txt","done_when":"written","type":"general"},
			{"key":"r1","title":"Read 1","instructions":"read","done_when":"read","type":"research"},
			{"key":"r2","title":"Read 2","instructions":"read","done_when":"read","type":"research"}],`+oneDecisionArg+`}`),
		call(workflow.ToolFinishVerification, passArgs),
	)
	// Every executor writes one file named after its task and then reports.
	// They run at the same time, so each is answered from its own conversation.
	x.provider.respond = func(request aicli.ChatRequest) (scriptedReply, bool) {
		if request.Model != cheapModel {
			return scriptedReply{}, false
		}
		if request.Messages[len(request.Messages)-1].Role == "tool" {
			return finish("done", "written"), true
		}
		name := "read"
		for _, title := range []string{"Write A", "Write B", "Read 1", "Read 2"} {
			if strings.Contains(request.Messages[1].Content, title) {
				name = strings.ReplaceAll(strings.ToLower(title), " ", "-")
			}
		}
		return call("write", `{"path":"`+name+`.txt","content":"x"}`), true
	}
	// Hold the cheap model until one writer and both readers are waiting on
	// it at once: that many sessions must be able to run together.
	hold := make(chan struct{})
	x.provider.hold = hold
	x.provider.holdModel = cheapModel
	go func() {
		for x.provider.inFlightFor(cheapModel) < 3 {
			time.Sleep(2 * time.Millisecond)
		}
		close(hold)
	}()

	x.start(root.ID)

	require.Equal(t, db.TaskStatusInReview, x.task(root.ID).Status)
	children := x.children(root.ID)
	require.Len(t, children, 4)
	worktree := x.paths().WorktreeDir("hc1", root.ID)
	var writers, readers []db.Run
	for _, child := range children {
		runs := x.runsOf(child.ID)
		require.Len(t, runs, 1)
		if workflow.WritesWorkspace(child.TaskType) {
			writers = append(writers, runs[0])
			assert.Equal(t, worktree, runs[0].WorkspacePath, "writers work in the tree's shared worktree")
		} else {
			readers = append(readers, runs[0])
			assert.NotEqual(t, worktree, runs[0].WorkspacePath, "readers do not")
		}
	}
	require.Len(t, writers, 2)
	require.Len(t, readers, 2)

	// Journal step IDs are one sequence across all tasks, so they order what
	// happened: when each session started and when its task took its result.
	stepOf := func(taskID int32, kind string) int64 {
		for _, step := range x.steps(taskID) {
			if step.Kind == kind {
				return step.ID
			}
		}
		t.Fatalf("task %d has no %s step", taskID, kind)
		return 0
	}
	firstWriter, secondWriter := writers[0].TaskID, writers[1].TaskID
	if stepOf(secondWriter, models.StepRunStarted) < stepOf(firstWriter, models.StepRunStarted) {
		firstWriter, secondWriter = secondWriter, firstWriter
	}
	firstDone := stepOf(firstWriter, models.StepRunFinished)
	assert.Greater(t, stepOf(secondWriter, models.StepRunStarted), firstDone, "the second writer started only after the first had finished")
	for _, reader := range readers {
		assert.Less(t, stepOf(reader.TaskID, models.StepRunStarted), firstDone, "readers started while the first writer was still at work")
	}
	assert.Equal(t, 3, x.provider.peakFor(cheapModel), "one writer and both readers were in flight together, and never both writers")

	entries, err := os.ReadDir(worktree)
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	assert.Equal(t, []string{"write-a.txt", "write-b.txt"}, names, "both writers' files are in the one worktree; the readers' are not")
	for i, reader := range readers {
		_, err := os.Stat(filepath.Join(reader.WorkspacePath, "read-"+strconv.Itoa(i+1)+".txt"))
		assert.NoError(t, err, "each reader wrote in its own scratch directory")
	}
	assert.Nil(t, x.task(root.ID).WorkspaceOwnerTaskID)
}

func TestExecutorPausesForARestartAndResumes(t *testing.T) {
	x := newExecutorFixture(t)
	task := x.direct(models.TaskTypeResearch, "Long job", "Do the long job.")
	x.provider.pushCheap(call("write", `{"path":"one.txt","content":"1"}`))
	// The server begins a planned shutdown while the first model call is in
	// flight: the session stops at the turn boundary, before running the tool.
	x.provider.hold = make(chan struct{})
	x.driver.Enqueue(task.ID)
	x.driver.runUntilIdle(context.Background())
	for len(x.provider.servedBy(cheapModel)) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	x.engine.runs.draining.Store(true)
	close(x.provider.hold)
	x.engine.runs.active.Wait()
	x.driver.runUntilIdle(context.Background())

	paused := x.runsOf(task.ID)
	require.Len(t, paused, 1)
	assert.Equal(t, db.RunStatusPaused, paused[0].Status)
	assert.Greater(t, paused[0].Recovery.CheckpointSequence, int64(0))
	waiting := x.task(task.ID)
	assert.Equal(t, db.TaskStatusInProgress, waiting.Status, "the task simply keeps waiting on its paused session")
	assert.Equal(t, models.TaskWaitRun, waiting.WaitingOn)
	_, err := os.Stat(filepath.Join(paused[0].WorkspacePath, "one.txt"))
	assert.True(t, os.IsNotExist(err), "the pending tool call has not run yet")

	// The new server resumes it from its log.
	x.provider.hold = nil
	x.restart()
	x.provider.pushCheap(finish("done", "Long job finished."))
	x.engine.resumePausedExecutors(context.Background())
	x.settle()

	done := x.task(task.ID)
	assert.Equal(t, db.TaskStatusDone, done.Status)
	assert.Equal(t, 1, done.AttemptsUsed, "a resumed session is the same attempt")
	runs := x.runsOf(task.ID)
	require.Len(t, runs, 1, "and the same session")
	assert.Equal(t, "completed", runs[0].Status)
	written, err := os.ReadFile(filepath.Join(runs[0].WorkspacePath, "one.txt"))
	require.NoError(t, err, "the tool call that was pending ran after the restart")
	assert.Equal(t, "1", string(written))

	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 2)
	resumed := requests[1].Messages
	assert.Equal(t, "Do the long job.", strings.Split(resumed[1].Content, "\n\n")[2], "the conversation was restored from the log")
	var sawResume bool
	for _, message := range resumed {
		if message.Role == "system" && strings.Contains(message.Content, "has been resumed") {
			sawResume = true
		}
	}
	assert.True(t, sawResume, "the model is told it was interrupted")
}

func TestDeadExecutorSessionIsReapedAndTheTaskRetried(t *testing.T) {
	x := newExecutorFixture(t)
	task := x.direct(models.TaskTypeResearch, "Job", "Do it.")
	// The process died mid-session: the run says running, nobody owns it, and
	// its heartbeat has stopped.
	stale := time.Now().Add(-10 * time.Minute)
	run := db.Run{TaskID: task.ID, AgentID: x.agents["Coder"].ID, Status: "running", StartedAt: stale, LastMessageTime: &stale, Attempt: 1}
	require.NoError(t, x.database.Create(&run).Error)
	require.NoError(t, x.database.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"status": db.TaskStatusInProgress, "phase": models.TaskPhaseExecute, "waiting_on": models.TaskWaitRun,
		"run_id": run.ID, "attempts_used": 1,
	}).Error)

	// A session that is merely slow — still heartbeating — is left alone.
	fresh := time.Now()
	require.NoError(t, x.database.Model(&db.Run{}).Where("id = ?", run.ID).UpdateColumn("last_message_time", &fresh).Error)
	x.driver.sweep(context.Background())
	x.settle()
	assert.Equal(t, "running", x.runsOf(task.ID)[0].Status)

	require.NoError(t, x.database.Model(&db.Run{}).Where("id = ?", run.ID).UpdateColumn("last_message_time", &stale).Error)
	x.provider.pushCheap(finish("done", "Did it on the second try."))
	x.driver.sweep(context.Background())
	x.settle()

	runs := x.runsOf(task.ID)
	require.Len(t, runs, 2)
	assert.Equal(t, "failed", runs[0].Status)
	assert.Equal(t, sessionStoppedResponding, runs[0].LogContent)
	assert.Equal(t, 2, runs[1].Attempt)
	done := x.task(task.ID)
	assert.Equal(t, db.TaskStatusDone, done.Status)
	assert.Equal(t, "Did it on the second try.", done.ResultSummary)
}

func TestExecutorSlotsBoundConcurrency(t *testing.T) {
	e := NewNativeEngine(nil, eventhub.NewHub())
	for i := 0; i < maxConcurrentExecutors; i++ {
		require.True(t, e.reserveExecutorSlot())
	}
	assert.False(t, e.reserveExecutorSlot(), "no slot beyond the limit")
	e.releaseExecutorSlot()
	assert.True(t, e.reserveExecutorSlot())
}

func TestCheckpointTracker(t *testing.T) {
	turn := func(names ...string) aicli.Message {
		message := aicli.Message{Role: "assistant"}
		for _, name := range names {
			message.ToolCalls = append(message.ToolCalls, aicli.ToolCall{Function: aicli.FuncCall{Name: name}})
		}
		return message
	}
	var history []aicli.Message
	tracker := checkpointTracker{every: 3}
	tracker.start(history)

	history = append(history, turn("ls"), aicli.Message{Role: "tool"}, turn("ls"))
	assert.False(t, tracker.due(history), "two calls: not yet")
	history = append(history, turn("read", "grep"))
	assert.True(t, tracker.due(history), "four calls since the start")
	assert.False(t, tracker.due(history), "it is demanded once, not on every look")
	assert.True(t, tracker.demanded)

	history = append(history, turn("checkpoint"))
	tracker.recorded()
	assert.False(t, tracker.due(history), "the count starts again from the checkpoint")
	history = append(history, turn("ls"), turn("ls"))
	assert.False(t, tracker.due(history))
	history = append(history, turn("ls"))
	assert.True(t, tracker.due(history))

	// A demand the model keeps ignoring is dropped until the next interval.
	assert.True(t, tracker.missed())
	assert.False(t, tracker.missed())
	assert.False(t, tracker.demanded)
	assert.False(t, tracker.due(history))
	history = append(history, turn("ls"), turn("ls"), turn("ls"))
	assert.True(t, tracker.due(history))

	// A session resumed from its log picks up from its last checkpoint.
	resumed := checkpointTracker{every: 3}
	resumed.start(append(history, turn("checkpoint"), turn("ls")))
	assert.Equal(t, 12, resumed.baseline, "twelve tool calls up to and including its last checkpoint")
	assert.False(t, resumed.due(append(history, turn("checkpoint"), turn("ls"))))

	off := checkpointTracker{}
	assert.False(t, off.due(history), "an interval of zero never demands")
}

func idText(n int32) string { return strconv.FormatInt(int64(n), 10) }

// A coding executor works in the tree's git worktree, on the task's branch,
// and its changes are committed for it when it reports done.
func TestExecutorCommitsACodingTaskToItsBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	x := newExecutorFixture(t)
	ctx := context.Background()
	git := func(dir string, args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := command.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	// The project's remote: a repository with one commit on main.
	remote := t.TempDir()
	git(remote, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(remote, "README.md"), []byte("# project\n"), 0o644))
	git(remote, "add", ".")
	git(remote, "commit", "-m", "initial")

	project := db.Project{CompanyID: x.company.ID, Name: "portal", RepositoryUrl: remote}
	require.NoError(t, x.database.Create(&project).Error)
	manager := filesystem.NewManager(loadSettings().BasePath)
	require.NoError(t, manager.PrepareProjectRepo(ctx, x.company, project, ""))

	coder := x.agents["Coder"].ID
	task, err := x.q.CreateTask(ctx, db.Task{
		CompanyID: x.company.ID, SprintID: x.sprint.ID, ProjectID: &project.ID, AgentID: &coder,
		Title: "Add a changelog", Description: "Create CHANGELOG.md.",
		TaskType: models.TaskTypeCoding, Mode: models.TaskModeDirect, Status: db.TaskStatusTodo,
	})
	require.NoError(t, err)
	x.provider.pushCheap(
		call("write", `{"path":"CHANGELOG.md","content":"## 1.0\n- first\n"}`),
		finish("done", "Added CHANGELOG.md"),
		scriptedReply{text: "Add a changelog\n\nRecords the first release."}, // the commit message
	)

	x.start(task.ID)

	done := x.task(task.ID)
	require.Equal(t, db.TaskStatusDone, done.Status)
	worktree := x.paths().WorktreeDir("hc1", task.ID)
	assert.Equal(t, worktree, x.runsOf(task.ID)[0].WorkspacePath)
	assert.Equal(t, done.GitHubBranch, git(worktree, "rev-parse", "--abbrev-ref", "HEAD"), "the worktree is on the task's branch")
	assert.Contains(t, git(worktree, "log", "-1", "--format=%B"), "Add a changelog", "the model's commit message was used")
	assert.Equal(t, "CHANGELOG.md", git(worktree, "show", "--name-only", "--format=", "HEAD"))
	assert.Empty(t, git(worktree, "status", "--porcelain"), "nothing is left uncommitted")
	assert.Equal(t, "2", git(worktree, "rev-list", "--count", "HEAD"), "one commit on top of the base branch")
	assert.Nil(t, done.WorkspaceOwnerTaskID, "the worktree is released once the commit is in")

	system := x.provider.servedBy(cheapModel)[0].Messages[0].Content
	assert.Contains(t, system, "This is a coding task")
	assert.Contains(t, system, "Do not commit")
	assert.Contains(t, system, "Git branch: "+done.GitHubBranch+" (based on main)")

	// The commit-message call is in the ledger under its own tier.
	byTier, err := x.q.UsageBy(ctx, db.UsageFilter{CompanyID: x.company.ID, TaskID: &task.ID}, db.UsageByTier)
	require.NoError(t, err)
	tiers := map[string]int64{}
	for _, group := range byTier {
		tiers[group.Key] = group.Calls
	}
	assert.Equal(t, map[string]int64{models.TierCheap: 2, models.TierCommit: 1}, tiers)
}

// What a human attached to a task reaches the session that does the work: it
// is told where the files are, and it can read them.
func TestExecutorCanReadTheTaskAttachments(t *testing.T) {
	x := newExecutorFixture(t)
	ctx := context.Background()
	task := x.direct(models.TaskTypeResearch, "Summarise the contract", "Say what the attached contract obliges us to.")
	uploads := x.paths().TaskUploadsDir(task.ID)
	require.NoError(t, os.MkdirAll(uploads, 0o755))
	contract := filepath.Join(uploads, "contract.txt")
	require.NoError(t, os.WriteFile(contract, []byte("We must deliver by May."), 0o644))
	_, err := x.q.CreateAttachment(ctx, db.Attachment{TaskID: task.ID, Filename: "contract.txt", FilePath: contract})
	require.NoError(t, err)
	x.provider.pushCheap(
		call("read", `{"path":"`+contract+`"}`),
		finish("done", "Delivery is due by May."),
	)

	x.start(task.ID)

	require.Equal(t, db.TaskStatusDone, x.task(task.ID).Status)
	requests := x.provider.servedBy(cheapModel)
	require.Len(t, requests, 2)
	assert.Contains(t, requests[0].Messages[0].Content, "Files the human attached to the task: "+contract)
	read := requests[1].Messages[len(requests[1].Messages)-1]
	assert.Equal(t, "tool", read.Role)
	assert.Contains(t, read.Content, "We must deliver by May.", "the attachment is readable from the session's sandbox")
}
