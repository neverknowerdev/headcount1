package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/mempalace"
)

// This file wires the MemPalace memory layer into agent sessions:
// tool-proxy scope resolution, the Memory system-prompt section, plan-mode
// recall injection, transcript mining and the run-teardown capture hook.
// Every hook degrades to a no-op when mempalace is unavailable.
//
// Design principle (from upstream MemPalace's own editor hooks): mechanical
// capture by the engine is the guarantee; agent-called tools add distilled
// judgment only.

// callMemoryTool is an indirection over mempalace.CallServerTool so engine
// tests can fake the palace.
var callMemoryTool = mempalace.CallServerTool

// mineTranscriptFn is an indirection over mempalace.MineTranscript for tests.
var mineTranscriptFn = mempalace.MineTranscript

// memorySession is the per-run memory context resolved once in executeSession.
type memorySession struct {
	server db.MCPServer
	scope  tools.MemoryScope

	// transcriptDir is the run's log directory holding the transcript JSONL
	// consumed by checkpoint/teardown/pre-prune mining ("" = no transcript).
	transcriptDir string
	// transcript is the session's writer, used only to count captured
	// messages so redundant mine passes can be skipped.
	transcript *aicli.TranscriptWriter

	// teardownOnce guards the teardown hook against double-fire (finish_task
	// followed by the post-loop terminal path).
	teardownOnce sync.Once

	// minedMessages is the transcript message count at the last mine, so
	// checkpoint and pre-prune mines skip when nothing new was captured.
	minedMessages atomic.Int64
}

// resolveMemorySession returns the memory context for a run, or nil when the
// memory layer is not available/ready for this company.
func (e *NativeEngine) resolveMemorySession(ctx context.Context, company db.Company, task db.Task, runID int32, agentName string) *memorySession {
	if !mempalace.Available() {
		return nil
	}
	server, err := mempalace.ServerForCompany(ctx, e.q, company)
	if err != nil || !mempalace.Ready(server) {
		return nil
	}
	var project *db.Project
	if task.ProjectID != nil {
		if p, pErr := e.q.GetProject(ctx, *task.ProjectID); pErr == nil {
			project = &p
		}
	}
	addr := mempalace.Resolve(company, project, &task, runID, agentName)
	taskPath := fmt.Sprintf("tasks/%s", strings.TrimPrefix(addr.Closet, "task-"))
	return &memorySession{
		server: server,
		scope: tools.MemoryScope{
			ProjectWing: addr.Wing,
			CompanyWing: mempalace.CompanyWing,
			AgentWing:   addr.AgentWing,
			Closet:      addr.Closet,
			TaskPath:    taskPath,
			RunID:       runID,
			AddedBy:     agentName,
			TaskEntity:  addr.Closet,
		},
	}
}

// memoryActivityRecorder returns the activity-feed callback for a run's
// memory proxy. Rows are written asynchronously — the feed must never slow a
// tool call down.
func (e *NativeEngine) memoryActivityRecorder(companyID, taskID, runID int32, agentName string) tools.MemoryActivityFunc {
	return func(tool, kind, wing, room, query string, resultN int, args, response string) {
		go func() {
			tid, rid := taskID, runID
			if _, err := e.q.CreateMemoryActivity(context.Background(), db.MemoryActivity{
				CompanyID: companyID,
				AgentName: agentName,
				TaskID:    &tid,
				RunID:     &rid,
				Tool:      tool,
				Kind:      kind,
				Wing:      wing,
				Room:      room,
				Query:     query,
				ResultN:   resultN,
				Args:      args,
				Response:  response,
			}); err != nil {
				fmt.Printf("Warning: failed to record memory activity: %v\n", err)
			}
		}()
	}
}

// memoryPromptSection renders the Palace Protocol block appended to the
// system prompt when memory is ready. The wake-up block is wing-level
// identity/context, cached an hour and capped in pkg/mempalace.
func (e *NativeEngine) memoryPromptSection(company db.Company, scope tools.MemoryScope) string {
	var b strings.Builder
	b.WriteString("\n\n## Memory\n")
	fmt.Fprintf(&b, "Project wing: %s.", scope.ProjectWing)
	if scope.Closet != "" {
		fmt.Fprintf(&b, " Current task closet: %s.", scope.Closet)
	}
	b.WriteString("\n")
	if wake := mempalace.WakeUp(company, scope.ProjectWing); wake != "" {
		b.WriteString(wake)
		b.WriteString("\n")
	}
	b.WriteString("Protocol: before asking a human or re-deriving a past decision, call recall_memory. " +
		"recall_memory accepts room='decisions' to search decisions only. " +
		"memory_facts lists structured facts (status, approach, blockers) for a task entity. ")
	if scope.TaskEntity != "" {
		fmt.Fprintf(&b, "Knowledge-graph entities are named task-<ref> (e.g. task-dec-62); the current task is %s. ", scope.TaskEntity)
	}
	b.WriteString("Use remember only for durable learnings no log can convey (e.g. 'X fails because Y — do Z instead'). " +
		"One fact per call, max 500 chars. The engine records run history, artifacts and status automatically. " +
		"Before finish_task, store the durable facts, decisions and learnings from this task that a future task or " +
		"agent would need — a few remember() calls, not a summary of everything you did. " +
		"When overturning an earlier decision, call memory_invalidate first (it also marks the old decision's text as superseded), then remember the new one.")
	return b.String()
}

// memorySearchResult mirrors the fields we consume from mempalace_search.
type memorySearchResult struct {
	Text      string  `json:"text"`
	Wing      string  `json:"wing"`
	Room      string  `json:"room"`
	CreatedAt string  `json:"created_at"`
	Sim       float64 `json:"similarity"`
}

// planModeRecall returns a "possibly relevant prior work" section for
// plan-mode (refinement) seeds, built from a project-scoped semantic search
// on the task text. Empty string when nothing relevant is found or on error.
func (e *NativeEngine) planModeRecall(ctx context.Context, mem *memorySession, task db.Task) string {
	query := task.Title
	if task.Description != "" {
		desc := task.Description
		if len(desc) > 180 {
			desc = desc[:180]
		}
		query += " " + desc
	}
	if len(query) > 250 {
		query = query[:250]
	}
	out, err := callMemoryTool(ctx, mem.server, "mempalace_search", map[string]any{
		"query": query, "wing": mem.scope.ProjectWing, "limit": 5,
	})
	if err != nil {
		return ""
	}
	var parsed struct {
		Results []memorySearchResult `json:"results"`
	}
	if jErr := json.Unmarshal([]byte(out), &parsed); jErr != nil || len(parsed.Results) == 0 {
		return ""
	}

	e.recordEngineMemoryActivity(task, mem, "engine:refinement-recall", "read", query, len(parsed.Results))

	var b strings.Builder
	b.WriteString("\n\n## Possibly relevant prior work/decisions (from memory)\n")
	const budget = 8000 // ~2k tokens
	for _, r := range parsed.Results {
		date := r.CreatedAt
		if len(date) > 10 {
			date = date[:10]
		}
		text := strings.TrimSpace(r.Text)
		if len(text) > 700 {
			text = text[:700] + "…"
		}
		entry := fmt.Sprintf("- [%s, %s] %s\n", date, r.Room, text)
		if b.Len()+len(entry) > budget {
			break
		}
		b.WriteString(entry)
	}
	b.WriteString("Use recall_memory for more context; verify anything critical before relying on it.")
	return b.String()
}

// storeRefinementMemory persists a completed plan-mode run's refinement as a
// decisions-room drawer plus knowledge-graph facts, superseding any previous
// plan for the same task (delete is for wrong, supersede is for outdated).
func (e *NativeEngine) storeRefinementMemory(ctx context.Context, mem *memorySession, task db.Task, runID int32, resultDetails string) {
	content := strings.TrimSpace(task.RefinedDescription)
	if content == "" {
		content = strings.TrimSpace(resultDetails)
	}
	if content == "" {
		return
	}
	if ac := formatSpecItems(task.AcceptanceCriteria); ac != "" {
		content += "\n\nAcceptance criteria:\n" + ac
	}
	// Markdown headings stored as KG objects poisoned the pilot's graph —
	// junk facts are worse than no facts, so an unusable summary skips the
	// fact (the drawer is still stored).
	summary := kgSummary(content, 140)

	// Supersede a previous plan drawer for this task, if one exists.
	e.supersedePreviousPlan(ctx, mem, runID)

	stored := fmt.Sprintf("[%s] [plan] Task %s: %s\n%s", mem.scope.Closet, task.RefKey, task.Title, content)
	if len(stored) > 8000 {
		stored = stored[:8000]
	}
	if _, err := callMemoryTool(ctx, mem.server, "mempalace_add_drawer", map[string]any{
		"wing":        mem.scope.ProjectWing,
		"room":        mempalace.RoomDecisions,
		"content":     stored,
		"source_file": fmt.Sprintf("%s/run-%d", mem.scope.TaskPath, runID),
		"added_by":    mem.scope.AddedBy,
	}); err != nil {
		fmt.Printf("Warning: failed to store refinement memory: %v\n", err)
		return
	}

	// Knowledge graph: close the previous approach fact, add the new one.
	e.addTaskFact(ctx, mem, "approach", summary, true)

	e.recordEngineMemoryActivity(task, mem, "engine:refinement-store", "write", summary, 1)
}

// addTaskFact stores one knowledge-graph fact about the current task entity.
// With supersede set, any current fact with the same predicate is invalidated
// first (temporal KG: one current value per predicate, history preserved) —
// used for status/approach; produced-artifact facts accumulate instead.
// Objects that kgSummary judged unusable ("") are skipped — junk facts are
// worse than no facts.
func (e *NativeEngine) addTaskFact(ctx context.Context, mem *memorySession, predicate, object string, supersede bool) bool {
	if object == "" || mem.scope.TaskEntity == "" {
		return false
	}
	if supersede {
		if out, err := callMemoryTool(ctx, mem.server, "mempalace_kg_query", map[string]any{"entity": mem.scope.TaskEntity}); err == nil {
			var parsed struct {
				Facts []struct {
					Subject   string `json:"subject"`
					Predicate string `json:"predicate"`
					Object    string `json:"object"`
					Current   bool   `json:"current"`
				} `json:"facts"`
			}
			if json.Unmarshal([]byte(out), &parsed) == nil {
				for _, f := range parsed.Facts {
					if f.Current && f.Predicate == predicate && f.Subject == mem.scope.TaskEntity {
						_, _ = callMemoryTool(ctx, mem.server, "mempalace_kg_invalidate", map[string]any{
							"subject": f.Subject, "predicate": f.Predicate, "object": f.Object,
						})
					}
				}
			}
		}
	}
	if _, err := callMemoryTool(ctx, mem.server, "mempalace_kg_add", map[string]any{
		"subject": mem.scope.TaskEntity, "predicate": predicate, "object": object,
	}); err != nil {
		fmt.Printf("Warning: failed to store %s KG fact: %v\n", predicate, err)
		return false
	}
	return true
}

// supersedePreviousPlan prefixes older plan drawers of this task's closet
// with a superseded marker so recall presents them as history, not truth.
func (e *NativeEngine) supersedePreviousPlan(ctx context.Context, mem *memorySession, newRunID int32) {
	out, err := callMemoryTool(ctx, mem.server, "mempalace_list_drawers", map[string]any{
		"wing": mem.scope.ProjectWing, "room": mempalace.RoomDecisions, "limit": 100,
	})
	if err != nil {
		return
	}
	var parsed struct {
		Drawers []struct {
			DrawerID string `json:"drawer_id"`
			Preview  string `json:"content_preview"`
		} `json:"drawers"`
	}
	if json.Unmarshal([]byte(out), &parsed) != nil {
		return
	}
	planPrefix := fmt.Sprintf("[%s] [plan]", mem.scope.Closet)
	for _, d := range parsed.Drawers {
		if strings.HasPrefix(d.Preview, planPrefix) {
			// Fetch full content so the update doesn't truncate to the preview.
			full, fErr := callMemoryTool(ctx, mem.server, "mempalace_get_drawer", map[string]any{"drawer_id": d.DrawerID})
			if fErr != nil {
				continue
			}
			var drawer struct {
				Content string `json:"content"`
			}
			if json.Unmarshal([]byte(full), &drawer) != nil || drawer.Content == "" {
				continue
			}
			if strings.HasPrefix(drawer.Content, "[SUPERSEDED") {
				continue
			}
			_, _ = callMemoryTool(ctx, mem.server, "mempalace_update_drawer", map[string]any{
				"drawer_id": d.DrawerID,
				"content":   fmt.Sprintf("[SUPERSEDED by run %d] %s", newRunID, drawer.Content),
			})
		}
	}
}

// autoDiary writes a minimal end-of-run diary entry when the run ended
// without an agent-written one. For runs that never reached finish_task
// (failed/canceled) it synthesizes from whatever context exists so no run
// leaves zero trace.
func (e *NativeEngine) autoDiary(ctx context.Context, mem *memorySession, task db.Task, status, resultDetails string) {
	var entry string
	if status == "completed" {
		entry = fmt.Sprintf("What happened: finished task %s (%q) with status %q.", task.RefKey, task.Title, status)
	} else {
		entry = fmt.Sprintf("What happened: run on task %s (%q) ended with status %q.", task.RefKey, task.Title, status)
	}
	if resultDetails != "" {
		if len(resultDetails) > 400 {
			resultDetails = resultDetails[:400] + "…"
		}
		entry += "\nResult: " + resultDetails
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	call := map[string]any{
		"agent_name": mempalace.SanitizeName(mem.scope.AddedBy),
		"entry":      entry,
	}
	if mem.scope.Closet != "" {
		call["topic"] = mem.scope.Closet
	}
	if _, err := callMemoryTool(ctx, mem.server, "mempalace_diary_write", call); err != nil {
		fmt.Printf("Warning: auto-diary write failed: %v\n", err)
		return
	}
	e.recordEngineMemoryActivity(task, mem, "engine:auto-diary", "write", firstLine(entry, 120), 1)
}

// memoryTeardown is the single capture hook fired on EVERY terminal
// transition of a run — finish_task, agent error, cancellation. It runs in a
// detached goroutine with its own timeout, fires at most once per run
// session, and swallows every failure: memory must never block or fail a run.
//
// mem may be nil (memory unavailable) — the call is then a no-op. mpProxy may
// be nil (no tool proxy was registered).
func (e *NativeEngine) memoryTeardown(mem *memorySession, mpProxy *tools.MempalaceProxy, task db.Task, runID int32, mode, terminalStatus, resultDetails string) {
	if mem == nil {
		return
	}
	mem.teardownOnce.Do(func() {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("Warning: memory teardown panicked: %v\n", r)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			e.runMemoryTeardown(ctx, mem, mpProxy, task, runID, mode, terminalStatus, resultDetails)
		}()
	})
}

// runMemoryTeardown does the actual teardown work, synchronously.
func (e *NativeEngine) runMemoryTeardown(ctx context.Context, mem *memorySession, mpProxy *tools.MempalaceProxy, task db.Task, runID int32, mode, terminalStatus, resultDetails string) {
	// Synthesize a result line for runs that never reached finish_task:
	// result description, else the agent's last status report, else nothing.
	detail := strings.TrimSpace(resultDetails)
	if detail == "" {
		if run, err := e.q.GetRun(ctx, runID); err == nil {
			if run.ResultDescription != "" {
				detail = run.ResultDescription
			} else {
				detail = run.CurrentStatus
			}
		}
	}

	// 1. Auto-diary — unless the agent already wrote one this run.
	if mpProxy == nil || !mpProxy.DiaryWritten() {
		e.autoDiary(ctx, mem, task, terminalStatus, detail)
	}

	// 2. Artifact ingestion — this run's artifacts become searchable drawers.
	produced := e.ingestRunArtifacts(ctx, mem, mpProxy, task, runID)

	// 3. Mechanical KG facts (no LLM). The status object is a controlled
	// vocabulary value ("completed", "failed", …), not free text — it goes in
	// as-is rather than through kgSummary's minimum-signal gate.
	factsAdded := 0
	if e.addTaskFact(ctx, mem, "status", firstLine(terminalStatus, 60), true) {
		factsAdded++
	}
	if strings.Contains(strings.ToLower(terminalStatus), "blocked") ||
		strings.Contains(strings.ToLower(detail), "blocked") {
		if e.addTaskFact(ctx, mem, "blocked_by", kgSummary(firstLine(detail, 400), 140), true) {
			factsAdded++
		}
	}
	for _, filename := range produced {
		if e.addTaskFact(ctx, mem, "produced", "artifact:"+filename, false) {
			factsAdded++
		}
	}
	if factsAdded > 0 {
		e.recordEngineMemoryActivity(task, mem, "engine:kg-facts", "write", terminalStatus, factsAdded)
	}

	// 4. Refinement store for plan-mode runs.
	if mode == "plan" {
		if t, err := e.q.GetTask(ctx, task.ID); err == nil {
			task = t
		}
		e.storeRefinementMemory(ctx, mem, task, runID, resultDetails)
	}

	// 5. Transcript mine — verbatim capture of the whole session.
	if n, err := e.mineSessionTranscript(ctx, mem); err != nil {
		fmt.Printf("Warning: teardown transcript mine failed: %v\n", err)
		e.recordEngineMemoryActivity(task, mem, "engine:transcript-mine", "write", "teardown", 0)
	} else if n > 0 {
		e.recordEngineMemoryActivity(task, mem, "engine:transcript-mine", "write", "teardown", 1)
	}
}

// ingestRunArtifacts stores a drawer per artifact created by this run so
// artifacts become visible to semantic recall (pilot finding: only diary text
// mentioning them was findable). Duplicate-guarded, so it is idempotent under
// teardown retries. Returns the ingested filenames.
func (e *NativeEngine) ingestRunArtifacts(ctx context.Context, mem *memorySession, mpProxy *tools.MempalaceProxy, task db.Task, runID int32) []string {
	arts, err := e.q.ListArtifactsByTaskTree(ctx, task.ID)
	if err != nil {
		return nil
	}
	var ingested []string
	for _, a := range arts {
		if a.RunID != runID {
			continue
		}
		content := fmt.Sprintf("[%s] [artifact %s] %s\n\n%s", mem.scope.Closet, a.Filename, a.Description, a.Content)
		if len(content) > 8000 {
			content = content[:8000]
		}
		if mpProxy != nil {
			if dup, _ := mpProxy.CheckDuplicate(ctx, content); dup {
				continue
			}
		}
		room := mempalace.RoomGeneral
		if strings.Contains(strings.ToLower(a.Description), "decision") {
			room = mempalace.RoomDecisions
		}
		if _, err := callMemoryTool(ctx, mem.server, "mempalace_add_drawer", map[string]any{
			"wing":        mem.scope.ProjectWing,
			"room":        room,
			"content":     content,
			"source_file": "artifacts/" + a.Filename,
			"added_by":    mem.scope.AddedBy,
		}); err != nil {
			fmt.Printf("Warning: artifact ingest failed for %s: %v\n", a.Filename, err)
			continue
		}
		ingested = append(ingested, a.Filename)
	}
	if len(ingested) > 0 {
		e.recordEngineMemoryActivity(task, mem, "engine:artifact-ingest", "write", strings.Join(ingested, ", "), len(ingested))
	}
	return ingested
}

// mineSessionTranscript runs the conversation miner over the run's transcript
// dir when new messages were captured since the last mine. Returns how many
// mine passes ran (0 or 1). Mining is idempotent (per-file sentinels), so
// concurrent/repeated calls are safe — just wasteful, hence the counter gate.
func (e *NativeEngine) mineSessionTranscript(ctx context.Context, mem *memorySession) (int, error) {
	if mem.transcriptDir == "" {
		return 0, nil
	}
	current := int64(mem.transcript.Count())
	if current == mem.minedMessages.Load() {
		return 0, nil
	}
	if err := mineTranscriptFn(ctx, mem.server, mem.scope.ProjectWing, mem.scope.AddedBy, mem.transcriptDir); err != nil {
		return 0, err
	}
	mem.minedMessages.Store(current)
	return 1, nil
}

func (e *NativeEngine) recordEngineMemoryActivity(task db.Task, mem *memorySession, tool, kind, query string, resultN int) {
	tid, rid := task.ID, mem.scope.RunID
	if _, err := e.q.CreateMemoryActivity(context.Background(), db.MemoryActivity{
		CompanyID: task.CompanyID,
		AgentName: "", // engine-initiated
		TaskID:    &tid,
		RunID:     &rid,
		Tool:      tool,
		Kind:      kind,
		Wing:      mem.scope.ProjectWing,
		Room:      "",
		Query:     query,
		ResultN:   resultN,
	}); err != nil {
		fmt.Printf("Warning: failed to record memory activity: %v\n", err)
	}
}

// checkpointMineInterval is how many assistant turns pass between background
// transcript mines (upstream MemPalace Stop-hook analog, SAVE_INTERVAL=15).
const checkpointMineInterval = 15

// memoryCheckpointHook returns the agent's OnAssistantTurn callback: every
// checkpointMineInterval assistant turns it kicks a background transcript
// mine so a long-running session's history is recallable before it ends.
func (e *NativeEngine) memoryCheckpointHook(mem *memorySession, task db.Task) func(int) {
	return func(turns int) {
		if turns%checkpointMineInterval != 0 {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			n, err := e.mineSessionTranscript(ctx, mem)
			if err != nil {
				fmt.Printf("Warning: checkpoint mine failed: %v\n", err)
				e.recordEngineMemoryActivity(task, mem, "engine:checkpoint-mine", "write", fmt.Sprintf("turn %d", turns), 0)
				return
			}
			if n > 0 {
				e.recordEngineMemoryActivity(task, mem, "engine:checkpoint-mine", "write", fmt.Sprintf("turn %d", turns), 1)
			}
		}()
	}
}

// memoryPrePruneHook returns the agent's OnPrune callback: a SYNCHRONOUS
// transcript mine before history pruning first truncates a message — the
// no-loss guarantee is this mine, not agent compliance. A mine failure is
// recorded and swallowed; the prune (and the run) always proceeds.
func (e *NativeEngine) memoryPrePruneHook(mem *memorySession, task db.Task) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		n, err := e.mineSessionTranscript(ctx, mem)
		if err != nil {
			fmt.Printf("Warning: pre-prune mine failed: %v\n", err)
			e.recordEngineMemoryActivity(task, mem, "engine:preprune-mine", "write", "", 0)
			return
		}
		if n > 0 {
			e.recordEngineMemoryActivity(task, mem, "engine:preprune-mine", "write", "", 1)
		}
	}
}

// listMarkerRe matches leading bullet/enumeration markers on a line.
var listMarkerRe = regexp.MustCompile(`^([-+>•]\s*|\d+[.)]\s+)+`)

// kgSummary distills free text into a knowledge-graph-safe object: markdown
// stripped, whitespace collapsed, capped at max chars. Returns "" when the
// residue has fewer than 20 alphabetic characters — the caller must then skip
// the fact entirely (the pilot stored a markdown heading as an entity).
func kgSummary(s string, max int) string {
	// Strip link syntax [text](url) → text, then residual markdown markers.
	for {
		start := strings.IndexByte(s, '[')
		if start < 0 {
			break
		}
		end := strings.IndexByte(s[start:], ']')
		if end < 0 {
			break
		}
		rest := s[start+end+1:]
		if !strings.HasPrefix(rest, "(") {
			// Not a link — drop just the brackets around the text.
			s = s[:start] + s[start+1:start+end] + rest
			continue
		}
		closeParen := strings.IndexByte(rest, ')')
		if closeParen < 0 {
			break
		}
		s = s[:start] + s[start+1:start+end] + rest[closeParen+1:]
	}
	s = strings.NewReplacer("#", "", "*", "", "`", "", "_", " ").Replace(s)

	// Drop leading list markers per line, collapse all whitespace.
	var words []string
	for _, line := range strings.Split(s, "\n") {
		line = listMarkerRe.ReplaceAllString(strings.TrimSpace(line), "")
		words = append(words, strings.Fields(line)...)
	}
	out := strings.Join(words, " ")

	alpha := 0
	for _, r := range out {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			alpha++
		}
	}
	if alpha < 20 {
		return ""
	}
	if len(out) > max {
		out = strings.TrimSpace(out[:max])
	}
	return out
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max]
	}
	return strings.TrimSpace(s)
}
