package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/mempalace"
)

// This file wires the MemPalace memory layer into agent sessions:
// tool-proxy scope resolution, the Memory system-prompt section, plan-mode
// recall injection, refinement persistence and end-of-run auto-diaries.
// Every hook degrades to a no-op when mempalace is unavailable.

// memorySession is the per-run memory context resolved once in executeSession.
type memorySession struct {
	server db.MCPServer
	scope  tools.MemoryScope
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
	return func(tool, kind, wing, room, query string, resultN int) {
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
		"Treat memory_facts (knowledge graph) as current truth; recalled drawer text is historical record — on conflict, facts win. " +
		"Store important decisions and learnings with remember. Before finish_task, call write_diary.")
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
	out, err := mempalace.CallServerTool(ctx, mem.server, "mempalace_search", map[string]any{
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
	summary := firstLine(content, 200)

	// Supersede a previous plan drawer for this task, if one exists.
	e.supersedePreviousPlan(ctx, mem, runID)

	stored := fmt.Sprintf("[%s] [plan] Task %s: %s\n%s", mem.scope.Closet, task.RefKey, task.Title, content)
	if len(stored) > 8000 {
		stored = stored[:8000]
	}
	if _, err := mempalace.CallServerTool(ctx, mem.server, "mempalace_add_drawer", map[string]any{
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
	if out, err := mempalace.CallServerTool(ctx, mem.server, "mempalace_kg_query", map[string]any{"entity": mem.scope.TaskEntity}); err == nil {
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
				if f.Current && f.Predicate == "approach" && f.Subject == mem.scope.TaskEntity {
					_, _ = mempalace.CallServerTool(ctx, mem.server, "mempalace_kg_invalidate", map[string]any{
						"subject": f.Subject, "predicate": f.Predicate, "object": f.Object,
					})
				}
			}
		}
	}
	if _, err := mempalace.CallServerTool(ctx, mem.server, "mempalace_kg_add", map[string]any{
		"subject": mem.scope.TaskEntity, "predicate": "approach", "object": summary,
	}); err != nil {
		fmt.Printf("Warning: failed to store refinement KG fact: %v\n", err)
	}

	e.recordEngineMemoryActivity(task, mem, "engine:refinement-store", "write", summary, 1)
}

// supersedePreviousPlan prefixes older plan drawers of this task's closet
// with a superseded marker so recall presents them as history, not truth.
func (e *NativeEngine) supersedePreviousPlan(ctx context.Context, mem *memorySession, newRunID int32) {
	out, err := mempalace.CallServerTool(ctx, mem.server, "mempalace_list_drawers", map[string]any{
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
			full, fErr := mempalace.CallServerTool(ctx, mem.server, "mempalace_get_drawer", map[string]any{"drawer_id": d.DrawerID})
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
			_, _ = mempalace.CallServerTool(ctx, mem.server, "mempalace_update_drawer", map[string]any{
				"drawer_id": d.DrawerID,
				"content":   fmt.Sprintf("[SUPERSEDED by run %d] %s", newRunID, drawer.Content),
			})
		}
	}
}

// autoDiary writes a minimal end-of-run diary entry when the agent finished
// the task without calling write_diary itself.
func (e *NativeEngine) autoDiary(mem *memorySession, task db.Task, status, resultDetails string) {
	entry := fmt.Sprintf("What happened: finished task %s (%q) with status %q.", task.RefKey, task.Title, status)
	if resultDetails != "" {
		if len(resultDetails) > 400 {
			resultDetails = resultDetails[:400] + "…"
		}
		entry += "\nResult: " + resultDetails
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	call := map[string]any{
		"agent_name": mempalace.SanitizeName(mem.scope.AddedBy),
		"entry":      entry,
	}
	if mem.scope.Closet != "" {
		call["topic"] = mem.scope.Closet
	}
	if _, err := mempalace.CallServerTool(ctx, mem.server, "mempalace_diary_write", call); err != nil {
		fmt.Printf("Warning: auto-diary write failed: %v\n", err)
		return
	}
	e.recordEngineMemoryActivity(task, mem, "engine:auto-diary", "write", firstLine(entry, 120), 1)
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

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max]
	}
	return strings.TrimSpace(s)
}
