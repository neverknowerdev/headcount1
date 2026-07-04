package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/eventhub"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// ---- kgSummary --------------------------------------------------------------

func TestKGSummaryStripsMarkdown(t *testing.T) {
	in := "## Task DEC-59: implement the *frontend* rewrite — `React 19` [docs](https://example.com) — Complete"
	out := kgSummary(in, 140)
	if out == "" {
		t.Fatal("expected a usable summary")
	}
	for _, bad := range []string{"#", "*", "`", "](", "http"} {
		if strings.Contains(out, bad) {
			t.Errorf("summary contains markdown remnant %q: %q", bad, out)
		}
	}
	if !strings.Contains(out, "Task DEC-59") || !strings.Contains(out, "docs") {
		t.Errorf("summary lost content: %q", out)
	}
}

func TestKGSummaryRejectsJunk(t *testing.T) {
	for _, in := range []string{"", "###", "- - -", "```", "x y z", "123 456 789", "## ok"} {
		if got := kgSummary(in, 140); got != "" {
			t.Errorf("kgSummary(%q) = %q, want \"\" (junk must be skipped)", in, got)
		}
	}
}

func TestKGSummaryCollapsesAndCaps(t *testing.T) {
	in := "- use SSE for realtime updates\n-   WebSockets rejected\t because of proxy buffering issues in production environments everywhere"
	out := kgSummary(in, 60)
	if len(out) > 60 {
		t.Errorf("summary exceeds cap: %d chars", len(out))
	}
	if strings.Contains(out, "\n") || strings.Contains(out, "  ") {
		t.Errorf("whitespace not collapsed: %q", out)
	}
	if !strings.HasPrefix(out, "use SSE") {
		t.Errorf("list marker not stripped: %q", out)
	}
}

// ---- teardown ---------------------------------------------------------------

// memFake fakes the mempalace MCP surface for engine hooks.
type memFake struct {
	mu    sync.Mutex
	calls []string         // tool names in order
	args  []map[string]any // parallel to calls
	kv    map[string]string
}

func (f *memFake) call(_ context.Context, _ db.MCPServer, tool string, args any) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(args)
	var m map[string]any
	json.Unmarshal(b, &m)
	f.calls = append(f.calls, tool)
	f.args = append(f.args, m)
	switch tool {
	case "mempalace_kg_query":
		return `{"facts": []}`, nil
	case "mempalace_check_duplicate":
		return `{"is_duplicate": false, "matches": []}`, nil
	}
	return "{}", nil
}

func (f *memFake) count(tool string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == tool {
			n++
		}
	}
	return n
}

func (f *memFake) argsFor(tool string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for i, c := range f.calls {
		if c == tool {
			out = append(out, f.args[i])
		}
	}
	return out
}

type teardownFixture struct {
	eng  *NativeEngine
	fake *memFake
	mem  *memorySession
	task db.Task
	run  db.Run
}

func newTeardownFixture(t *testing.T) *teardownFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&db.Company{}, &db.Project{}, &db.Agent{}, &db.Task{}, &db.Run{}, &db.Artifact{}, &db.MemoryActivity{}); err != nil {
		t.Fatal(err)
	}
	q := db.New(database)
	ctx := context.Background()

	company, _ := q.CreateCompany(ctx, "Acme")
	company.ShortName = "acme"
	agent, _ := q.CreateAgent(ctx, db.Agent{Name: "Coder", CompanyID: company.ID})
	task, _ := q.CreateTask(ctx, db.Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "Fix the build", RefKey: "DEC-62", Status: "in-progress"})
	run, _ := q.CreateRun(ctx, db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running"})

	fake := &memFake{}
	origCall, origMine := callMemoryTool, mineTranscriptFn
	callMemoryTool = fake.call
	mineTranscriptFn = func(ctx context.Context, company db.Company, wing, agentName, dir string) error {
		fake.call(ctx, db.MCPServer{}, "mine_transcript", map[string]any{"dir": dir, "wing": wing})
		return nil
	}
	t.Cleanup(func() { callMemoryTool, mineTranscriptFn = origCall, origMine })

	mem := &memorySession{
		server:  db.MCPServer{Name: "mempalace-acme"},
		company: company,
		scope: tools.MemoryScope{
			ProjectWing: "project-app",
			CompanyWing: "company",
			Closet:      "task-dec-62",
			TaskPath:    "tasks/dec-62",
			RunID:       run.ID,
			AddedBy:     "Coder",
			TaskEntity:  "task-dec-62",
		},
	}
	return &teardownFixture{
		eng:  &NativeEngine{q: q, hub: eventhub.NewHub()},
		fake: fake,
		mem:  mem,
		task: task,
		run:  run,
	}
}

func (fx *teardownFixture) activities(t *testing.T) map[string]int {
	t.Helper()
	acts, _, err := fx.eng.q.ListMemoryActivity(context.Background(), fx.task.CompanyID, "", "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	byTool := map[string]int{}
	for _, a := range acts {
		byTool[a.Tool]++
	}
	return byTool
}

// waitFor polls cond for up to ~2s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestMemoryTeardownFailedRun(t *testing.T) {
	fx := newTeardownFixture(t)
	ctx := context.Background()

	// An artifact written by this run.
	if _, err := fx.eng.q.CreateArtifact(ctx, db.Artifact{
		TaskID: fx.task.ID, RunID: fx.run.ID,
		Filename: "report.md", FilePath: "/tmp/report.md",
		Content: "findings", Description: "final report",
	}); err != nil {
		t.Fatal(err)
	}

	fx.eng.runMemoryTeardown(ctx, fx.mem, nil, fx.task, fx.run.ID, "execute", "failed", "LLM call failed: connection reset")

	if n := fx.fake.count("mempalace_diary_write"); n != 1 {
		t.Errorf("diary writes = %d, want 1", n)
	}
	diary, _ := fx.fake.argsFor("mempalace_diary_write")[0]["entry"].(string)
	if !strings.Contains(diary, `"failed"`) || !strings.Contains(diary, "connection reset") {
		t.Errorf("auto-diary should mention status and error, got %q", diary)
	}

	// Artifact ingested as a drawer.
	drawers := fx.fake.argsFor("mempalace_add_drawer")
	if len(drawers) != 1 {
		t.Fatalf("drawers stored = %d, want 1", len(drawers))
	}
	content, _ := drawers[0]["content"].(string)
	if !strings.Contains(content, "[artifact report.md]") || !strings.Contains(content, "findings") {
		t.Errorf("artifact drawer content = %q", content)
	}
	if sf := drawers[0]["source_file"]; sf != "artifacts/report.md" {
		t.Errorf("source_file = %v", sf)
	}

	// KG: status fact + produced fact, correctly named entity, no markdown.
	kgAdds := fx.fake.argsFor("mempalace_kg_add")
	found := map[string]string{}
	for _, a := range kgAdds {
		if a["subject"] != "task-dec-62" {
			t.Errorf("KG subject = %v, want task-dec-62", a["subject"])
		}
		found[a["predicate"].(string)] = a["object"].(string)
	}
	if found["status"] != "failed" {
		t.Errorf("status fact = %q, want failed", found["status"])
	}
	if found["produced"] != "artifact:report.md" {
		t.Errorf("produced fact = %q", found["produced"])
	}

	acts := fx.activities(t)
	for _, tool := range []string{"engine:auto-diary", "engine:artifact-ingest", "engine:kg-facts"} {
		if acts[tool] == 0 {
			t.Errorf("missing activity row %s (have %v)", tool, acts)
		}
	}
}

func TestMemoryTeardownBlockedFact(t *testing.T) {
	fx := newTeardownFixture(t)
	fx.eng.runMemoryTeardown(context.Background(), fx.mem, nil, fx.task, fx.run.ID,
		"execute", "blocked", "Blocked by missing production API credentials for the payment provider\nmore detail")

	var blocked string
	for _, a := range fx.fake.argsFor("mempalace_kg_add") {
		if a["predicate"] == "blocked_by" {
			blocked = a["object"].(string)
		}
	}
	if blocked == "" {
		t.Fatal("blocked_by fact missing")
	}
	if strings.Contains(blocked, "\n") || len(blocked) > 140 {
		t.Errorf("blocked_by object not summarized: %q", blocked)
	}
}

func TestMemoryTeardownDoubleFireGuard(t *testing.T) {
	fx := newTeardownFixture(t)

	done := make(chan struct{}, 2)
	orig := mineTranscriptFn
	mineTranscriptFn = func(ctx context.Context, company db.Company, wing, agentName, dir string) error {
		return nil
	}
	defer func() { mineTranscriptFn = orig }()

	// Wrap the fake so we can tell when a teardown pass finishes: diary write
	// is the first step, kg_add the last write of a pass without artifacts.
	fx.mem.transcriptDir = "" // skip mining
	go func() {
		fx.eng.memoryTeardown(fx.mem, nil, fx.task, fx.run.ID, "execute", "completed", "all done, tests green and shipped")
		done <- struct{}{}
	}()
	go func() {
		fx.eng.memoryTeardown(fx.mem, nil, fx.task, fx.run.ID, "execute", "completed", "all done, tests green and shipped")
		done <- struct{}{}
	}()
	<-done
	<-done

	// The teardown body runs detached — wait for the single pass to finish.
	waitFor(t, func() bool { return fx.fake.count("mempalace_diary_write") >= 1 })

	if n := fx.fake.count("mempalace_diary_write"); n != 1 {
		t.Errorf("diary writes = %d, want exactly 1 (double-fire guard)", n)
	}

	// Nil session is a no-op, not a panic.
	fx.eng.memoryTeardown(nil, nil, fx.task, fx.run.ID, "execute", "completed", "")
}

func TestMemoryTeardownSkipsDiaryWhenAgentWroteOne(t *testing.T) {
	fx := newTeardownFixture(t)
	proxy := tools.NewMempalaceProxy(fx.mem.server, fx.mem.scope, nil)
	proxy.MarkDiaryWritten()
	fx.eng.runMemoryTeardown(context.Background(), fx.mem, proxy, fx.task, fx.run.ID, "execute", "completed", "done")
	if n := fx.fake.count("mempalace_diary_write"); n != 0 {
		t.Errorf("auto-diary written despite agent diary (n=%d)", n)
	}
}

func TestMemoryPromptSectionMentionsEntity(t *testing.T) {
	fx := newTeardownFixture(t)
	section := fx.eng.memoryPromptSection(fx.mem.company, fx.mem.scope)
	for _, want := range []string{"task-dec-62", "memory_facts", "room='decisions'", "One fact per call"} {
		if !strings.Contains(section, want) {
			t.Errorf("prompt section missing %q", want)
		}
	}
	for _, banned := range []string{"write_diary", "facts win"} {
		if strings.Contains(section, banned) {
			t.Errorf("prompt section still contains %q", banned)
		}
	}
}
