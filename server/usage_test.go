package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/engine/enginetest"
	endpoints "agent-orchestrator/server/controllers"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// usageFixture is a task tree with a known bill: a root whose refinement asked
// a research subtask, then a planning call, an executor session and a failed
// call. Another tenant has usage of its own that must never show up.
type usageFixture struct {
	t        *testing.T
	database *gorm.DB
	router   chi.Router
	company  db.Company
	root     db.Task
	research db.Task
	step     db.TaskStep
	run      db.Run
	foreign  db.LLMCall
	calls    map[string]db.LLMCall
}

func newUsageFixture(t *testing.T) *usageFixture {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	q := db.New(database)
	ctx := context.Background()
	f := &usageFixture{t: t, database: database, calls: map[string]db.LLMCall{}}

	uid := testSeedUserID(t, q)
	f.company = db.Company{Name: "Acme", ShortName: "acme", UserID: &uid}
	require.NoError(t, database.Create(&f.company).Error)
	cto := db.Agent{CompanyID: f.company.ID, Name: "CTO"}
	require.NoError(t, database.Create(&cto).Error)
	coder := db.Agent{CompanyID: f.company.ID, Name: "Coder"}
	require.NoError(t, database.Create(&coder).Error)
	f.root, err = q.CreateTask(ctx, db.Task{CompanyID: f.company.ID, Title: "root"})
	require.NoError(t, err)
	f.research, err = q.CreateTask(ctx, db.Task{CompanyID: f.company.ID, Title: "look it up", ParentID: &f.root.ID})
	require.NoError(t, err)
	f.step, err = q.AppendTaskStep(ctx, db.TaskStep{TaskID: f.root.ID, RootTaskID: f.root.ID, Kind: "smart_call", Phase: "plan", Prompt: "the planning prompt", Response: "the planning answer"})
	require.NoError(t, err)
	// The session's log as the engine writes it: each turn is a request, the
	// provider's response, the assistant message (the position a call is
	// recorded at) and the tool results, some of which carry no position.
	f.run = db.Run{TaskID: f.research.ID, AgentID: coder.ID, Status: "completed", LogEntries: `[
		{"seq":1,"type":"info","content":"brief"},
		{"seq":2,"type":"request","content":"request 1"},
		{"seq":3,"type":"response","content":"response 1"},
		{"seq":4,"type":"message","content":"first answer"},
		{"type":"tool_call","content":"ls"},
		{"seq":5,"type":"tool_response","content":"ls output"},
		{"seq":6,"type":"request","content":"request 2"},
		{"seq":7,"type":"response","content":"response 2"},
		{"seq":8,"type":"message","content":"second answer"},
		{"seq":9,"type":"tool_response","content":"grep output"},
		{"seq":10,"type":"request","content":"request 3"},
		{"seq":11,"type":"response","content":"response 3"},
		{"seq":12,"type":"message","content":"third answer"},
		{"seq":13,"type":"outcome","content":"done"}]`}
	require.NoError(t, database.Create(&f.run).Error)

	day := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seq := func(n int64) *int64 { return &n }
	record := func(name string, call db.LLMCall) {
		call.CompanyID = f.company.ID
		call.RootTaskID = &f.root.ID
		if call.Status == "" {
			call.Status = "ok"
		}
		stored, err := q.RecordLLMCall(ctx, call)
		require.NoError(t, err)
		f.calls[name] = stored
	}
	record("refine", db.LLMCall{TaskID: &f.root.ID, AgentID: &cto.ID, AgentName: "CTO", Tier: "smart", Phase: "refine", WorkflowPhase: "refine", Model: "strong", PromptTokens: 100, CompletionTokens: 10, CreatedAt: day})
	record("turn1", db.LLMCall{TaskID: &f.research.ID, AgentID: &coder.ID, AgentName: "Coder", Tier: "cheap", Phase: "execute", WorkflowPhase: "refine", Model: "small", RunID: &f.run.ID, LogSeq: seq(4), PromptTokens: 20, CompletionTokens: 2, CreatedAt: day})
	record("turn2", db.LLMCall{TaskID: &f.research.ID, AgentID: &coder.ID, AgentName: "Coder", Tier: "cheap", Phase: "execute", WorkflowPhase: "refine", Model: "small", RunID: &f.run.ID, LogSeq: seq(8), PromptTokens: 30, CompletionTokens: 3, CreatedAt: day})
	record("turn3", db.LLMCall{TaskID: &f.research.ID, AgentID: &coder.ID, AgentName: "Coder", Tier: "cheap", Phase: "execute", WorkflowPhase: "refine", Model: "small", RunID: &f.run.ID, LogSeq: seq(12), PromptTokens: 40, CompletionTokens: 4, CreatedAt: day})
	record("plan", db.LLMCall{TaskID: &f.root.ID, AgentID: &cto.ID, AgentName: "CTO", Tier: "smart", Phase: "plan", WorkflowPhase: "plan", Model: "strong", StepID: &f.step.ID, PromptTokens: 200, CompletionTokens: 20, CreatedAt: day.AddDate(0, 0, 1)})
	record("failed", db.LLMCall{TaskID: &f.root.ID, AgentID: &cto.ID, AgentName: "CTO", Tier: "smart", Phase: "plan", WorkflowPhase: "plan", Model: "strong", Status: "error", Error: "timeout", CreatedAt: day.AddDate(0, 0, 1)})

	stranger, err := q.CreateUser(ctx, "stranger@test.local")
	require.NoError(t, err)
	theirs := db.Company{Name: "Theirs", ShortName: "theirs", UserID: &stranger.ID}
	require.NoError(t, database.Create(&theirs).Error)
	f.foreign, err = q.RecordLLMCall(ctx, db.LLMCall{CompanyID: theirs.ID, Tier: "smart", Model: "strong", PromptTokens: 9999, Status: "ok", CreatedAt: day})
	require.NoError(t, err)

	api := endpoints.NewAPI(database, &enginetest.Recorder{}, nil)
	r := chi.NewRouter()
	r.Get("/usage", api.GetUsage)
	r.Get("/usage/calls", api.ListUsageCalls)
	r.Get("/usage/calls/{id}", api.GetUsageCall)
	r.Route("/tasks/{id}", func(r chi.Router) {
		r.Use(api.LoadTask)
		r.Get("/usage", api.GetTaskUsage)
	})
	f.router = withTestUser(t, database, r)
	return f
}

type usageReport struct {
	Totals db.UsageTotals             `json:"totals"`
	Groups map[string][]db.UsageGroup `json:"groups"`
}

func (f *usageFixture) get(path string, into any) int {
	f.t.Helper()
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code == http.StatusOK && into != nil {
		require.NoError(f.t, json.Unmarshal(w.Body.Bytes(), into), w.Body.String())
	}
	return w.Code
}

func groupTokens(groups []db.UsageGroup) map[string]int64 {
	out := map[string]int64{}
	for _, group := range groups {
		out[group.Key] = group.PromptTokens
	}
	return out
}

// Every breakdown of the same usage adds up to the same total, and work a
// phase delegated is billed to that phase.
func TestUsageTotalsAndBreakdownsAgree(t *testing.T) {
	f := newUsageFixture(t)
	company := fmt.Sprintf("/usage?company_id=%d", f.company.ID)

	var report usageReport
	require.Equal(t, http.StatusOK, f.get(company+"&group_by=phase,phase_tier,agent,model,tier,day,root_task", &report))
	require.Equal(t, int64(6), report.Totals.Calls)
	require.Equal(t, int64(1), report.Totals.FailedCalls)
	require.Equal(t, int64(390), report.Totals.PromptTokens, "another tenant's usage is not counted")
	require.Equal(t, int64(39), report.Totals.CompletionTokens)
	for dimension, groups := range report.Groups {
		var sum int64
		for _, group := range groups {
			sum += group.PromptTokens
		}
		require.Equal(t, report.Totals.PromptTokens, sum, "breakdown by %s", dimension)
	}
	require.Equal(t, map[string]int64{"refine": 190, "plan": 200}, groupTokens(report.Groups["phase"]),
		"the research a refinement asked for is billed to refine")
	require.Equal(t, map[string]int64{"refine/smart": 100, "refine/cheap": 90, "plan/smart": 200}, groupTokens(report.Groups["phase_tier"]))
	require.Equal(t, map[string]int64{"strong": 300, "small": 90}, groupTokens(report.Groups["model"]))
	require.Equal(t, map[string]int64{"2026-10-01": 190, "2026-10-02": 200}, groupTokens(report.Groups["day"]))
	require.Len(t, report.Groups["root_task"], 1)

	// Filters narrow the same ledger.
	require.Equal(t, http.StatusOK, f.get(company+"&from=2026-10-02&group_by=tier", &report))
	require.Equal(t, int64(200), report.Totals.PromptTokens)
	require.Equal(t, http.StatusOK, f.get(company+"&tier=cheap", &report))
	require.Equal(t, int64(90), report.Totals.PromptTokens)
	require.Equal(t, http.StatusOK, f.get(company+fmt.Sprintf("&task_id=%d", f.root.ID), &report))
	require.Equal(t, int64(300), report.Totals.PromptTokens)
	require.Equal(t, http.StatusOK, f.get(company+fmt.Sprintf("&task_id=%d&subtree=true", f.root.ID), &report))
	require.Equal(t, int64(390), report.Totals.PromptTokens)

	require.Equal(t, http.StatusBadRequest, f.get(company+"&group_by=nonsense", nil))
	require.Equal(t, http.StatusBadRequest, f.get(company+"&from=yesterday", nil))
	require.Equal(t, http.StatusBadRequest, f.get("/usage", nil))
}

func TestTaskUsageRollsUpItsSubtasks(t *testing.T) {
	f := newUsageFixture(t)
	var report usageReport
	require.Equal(t, http.StatusOK, f.get(fmt.Sprintf("/tasks/%d/usage", f.root.ID), &report))
	require.Equal(t, int64(390), report.Totals.PromptTokens)
	require.Equal(t, map[string]int64{fmt.Sprint(f.root.ID): 300, fmt.Sprint(f.research.ID): 90}, groupTokens(report.Groups["task"]))
	require.Contains(t, report.Groups, "phase_tier")
	require.Contains(t, report.Groups, "agent")
	require.Contains(t, report.Groups, "model")

	require.Equal(t, http.StatusOK, f.get(fmt.Sprintf("/tasks/%d/usage?subtree=false", f.root.ID), &report))
	require.Equal(t, int64(300), report.Totals.PromptTokens)
	require.Equal(t, http.StatusOK, f.get(fmt.Sprintf("/tasks/%d/usage", f.research.ID), &report))
	require.Equal(t, int64(90), report.Totals.PromptTokens)
}

// Any usage row expands to the calls behind it, and each call opens as a log.
func TestUsageDrillsDownToCallsAndTheirLogs(t *testing.T) {
	f := newUsageFixture(t)
	company := fmt.Sprintf("/usage/calls?company_id=%d", f.company.ID)

	var page struct {
		Calls      []db.LLMCall `json:"calls"`
		NextBefore int64        `json:"next_before"`
	}
	require.Equal(t, http.StatusOK, f.get(company+"&limit=4", &page))
	require.Len(t, page.Calls, 4)
	require.Equal(t, f.calls["failed"].ID, page.Calls[0].ID, "newest first")
	require.NotZero(t, page.NextBefore)
	rest := page
	rest.NextBefore = 0
	require.Equal(t, http.StatusOK, f.get(company+fmt.Sprintf("&limit=4&before=%d", page.NextBefore), &rest))
	require.Len(t, rest.Calls, 2)
	require.Zero(t, rest.NextBefore, "the last page names no next one")

	require.Equal(t, http.StatusOK, f.get(company+"&phase=refine&tier=cheap", &page))
	require.Len(t, page.Calls, 3)

	var opened struct {
		Call    db.LLMCall       `json:"call"`
		Step    *db.TaskStep     `json:"step"`
		Entries []map[string]any `json:"entries"`
	}
	// A smart call opens as its journal step, with the whole prompt and answer.
	require.Equal(t, http.StatusOK, f.get(fmt.Sprintf("/usage/calls/%d", f.calls["plan"].ID), &opened))
	require.NotNil(t, opened.Step)
	require.Equal(t, "the planning prompt", opened.Step.Prompt)
	require.Equal(t, "the planning answer", opened.Step.Response)
	require.Empty(t, opened.Entries)

	// An executor turn opens as its own stretch of the session's log: its
	// request, the answer, and the results of the tools that answer called.
	// Nothing of the turn before or after it.
	contents := func(path string) []string {
		opened.Step, opened.Entries = nil, nil
		require.Equal(t, http.StatusOK, f.get(path, &opened))
		var out []string
		for _, entry := range opened.Entries {
			out = append(out, entry["content"].(string))
		}
		return out
	}
	require.Equal(t, []string{"brief", "request 1", "response 1", "first answer", "ls", "ls output"}, contents(fmt.Sprintf("/usage/calls/%d", f.calls["turn1"].ID)))
	require.Equal(t, []string{"request 2", "response 2", "second answer", "grep output"}, contents(fmt.Sprintf("/usage/calls/%d", f.calls["turn2"].ID)))
	require.Equal(t, []string{"request 3", "response 3", "third answer", "done"}, contents(fmt.Sprintf("/usage/calls/%d", f.calls["turn3"].ID)))
	require.Nil(t, opened.Step)
}

// Usage never crosses a tenant: not as a total, not as a list, not as a call.
func TestUsageIsTenantScoped(t *testing.T) {
	f := newUsageFixture(t)
	require.Equal(t, http.StatusNotFound, f.get(fmt.Sprintf("/usage?company_id=%d", f.foreign.CompanyID), nil))
	require.Equal(t, http.StatusNotFound, f.get(fmt.Sprintf("/usage/calls?company_id=%d", f.foreign.CompanyID), nil))
	require.Equal(t, http.StatusNotFound, f.get(fmt.Sprintf("/usage/calls/%d", f.foreign.ID), nil))
	require.Equal(t, http.StatusNotFound, f.get("/usage/calls/999999", nil))
}
