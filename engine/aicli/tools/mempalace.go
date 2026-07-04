package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/pkg/mempalace"
)

// MemoryScope is the engine-resolved addressing context for memory tools.
// The agent never names wings/rooms/closets itself — scope is injected
// server-side so taxonomy discipline is code, not prompt discipline.
type MemoryScope struct {
	ProjectWing string // "" → falls back to the company wing
	CompanyWing string
	AgentWing   string
	Closet      string // "task-<ref-key>", "" when no task context
	TaskPath    string // "tasks/<ref-key>" — source_file prefix for run recall
	RunID       int32  // current run id
	AddedBy     string // agent display name
	TaskEntity  string // default knowledge-graph entity (the task closet)
}

// wing returns the default search/write wing for this scope.
func (s MemoryScope) wing() string {
	if s.ProjectWing != "" {
		return s.ProjectWing
	}
	return s.CompanyWing
}

func (s MemoryScope) sourceFile(runID int32) string {
	if s.TaskPath == "" {
		return ""
	}
	if runID <= 0 {
		return s.TaskPath
	}
	return fmt.Sprintf("%s/run-%d", s.TaskPath, runID)
}

// MemoryActivityFunc records one memory operation for the activity feed.
type MemoryActivityFunc func(tool, kind, wing, room, query string, resultN int)

// MempalaceProxy exposes curated memory tools backed by the company's
// mempalace MCP server (codegraph-proxy pattern: register on the run's
// registry, engine injects all scoping). The underlying stdio client is the
// package-level cached one in pkg/mempalace, shared with the engine hooks and
// the /api/memory handlers so all palace writes serialize on one lock.
type MempalaceProxy struct {
	server       db.MCPServer
	scope        MemoryScope
	onActivity   MemoryActivityFunc
	diaryWritten atomic.Bool
	// callFn dispatches one mempalace MCP tool call; defaults to
	// mempalace.CallServerTool and is only overridden in tests.
	callFn func(ctx context.Context, server db.MCPServer, tool string, args any) (string, error)
}

// NewMempalaceProxy creates a proxy bound to one company palace and one
// run's scope. onActivity may be nil.
func NewMempalaceProxy(server db.MCPServer, scope MemoryScope, onActivity MemoryActivityFunc) *MempalaceProxy {
	return &MempalaceProxy{server: server, scope: scope, onActivity: onActivity, callFn: mempalace.CallServerTool}
}

// DiaryWritten reports whether the agent wrote a diary entry during the run —
// the engine writes an auto-diary at teardown when it didn't. The write_diary
// tool is no longer in the default catalog, but the flag stays meaningful for
// roles that re-enable it via AllowedTools.
func (p *MempalaceProxy) DiaryWritten() bool { return p.diaryWritten.Load() }

// MarkDiaryWritten records that a diary entry exists for this run. Set by a
// (re-enabled) write_diary tool or tests; suppresses the teardown auto-diary.
func (p *MempalaceProxy) MarkDiaryWritten() { p.diaryWritten.Store(true) }

// RegisterAll adds the curated memory tools to the registry. Role gating
// happens through the agent config's AllowedTools filter, which the engine
// applies after registration (memory_invalidate / recall_company are listed
// only for planner-tier roles). Returns a one-line summary for the run log.
func (p *MempalaceProxy) RegisterAll(r *aicli.Registry) string {
	names := make([]string, 0, len(mpCatalog))
	for _, spec := range mpCatalog {
		r.Register(&mpProxyTool{proxy: p, spec: spec})
		names = append(names, spec.name)
	}
	return fmt.Sprintf("Memory (wing %s): registered %s", p.scope.wing(), strings.Join(names, ", "))
}

// Close releases nothing today (the MCP client is shared and cached at the
// package level), but mirrors the codegraph proxy lifecycle so the engine
// treats both uniformly.
func (p *MempalaceProxy) Close() {}

func (p *MempalaceProxy) record(tool, kind, room, query string, resultN int) {
	if p.onActivity != nil {
		p.onActivity(tool, kind, p.scope.wing(), room, query, resultN)
	}
}

func (p *MempalaceProxy) call(ctx context.Context, mcpTool string, args map[string]any) (string, error) {
	out, err := p.callFn(ctx, p.server, mcpTool, args)
	if err != nil {
		// Memory must degrade gracefully mid-run: surface the failure to the
		// model as a tool error string; never abort the session.
		return "", fmt.Errorf("memory unavailable: %w", err)
	}
	return out, nil
}

// ---- tool catalog ----

// rememberMaxChars caps remember content so each memory stays one atomic
// fact and always fits below the palace chunk size (never split mid-fact).
const rememberMaxChars = 500

// rememberDupThreshold is the cosine-similarity threshold above which content
// counts as a duplicate. The pilot showed 0.95 misses re-phrasings of the
// same fact ("no agent has shell tools" stored 7×). Measured with the shipped
// MiniLM embeddings, comparing stored-form vs stored-form (both carrying the
// "[closet] [kind]" prefix): same-task paraphrases score ≈0.83, cross-task
// paraphrases ≈0.75, genuinely different facts with shared vocabulary ≈0.54.
// 0.72 catches both paraphrase classes with margin against false positives.
// Tune with the paraphrase-gauntlet e2e test.
const rememberDupThreshold = 0.72

// CheckDuplicate reports whether content is already stored in the palace and
// returns the most similar existing text (may be empty). Primary path is
// mempalace_check_duplicate; when that is inconclusive (vector index down,
// error, unparseable) it falls back to a wing-scoped top-1 semantic search.
// Both remember and the engine's artifact ingestion use this one guard.
func (p *MempalaceProxy) CheckDuplicate(ctx context.Context, content string) (bool, string) {
	out, err := p.call(ctx, "mempalace_check_duplicate", map[string]any{
		"content": content, "threshold": rememberDupThreshold,
	})
	if err == nil {
		var parsed struct {
			Duplicate      *bool `json:"duplicate"`
			IsDuplicate    *bool `json:"is_duplicate"`
			VectorDisabled bool  `json:"vector_disabled"`
			Matches        []struct {
				Content string `json:"content"`
			} `json:"matches"`
		}
		if json.Unmarshal([]byte(out), &parsed) == nil && !parsed.VectorDisabled &&
			(parsed.Duplicate != nil || parsed.IsDuplicate != nil) {
			existing := ""
			if len(parsed.Matches) > 0 {
				existing = parsed.Matches[0].Content
			}
			return isDuplicate(out), existing
		}
	}

	// Inconclusive/unsupported — fall back to a wing-scoped search.
	out, err = p.call(ctx, "mempalace_search", map[string]any{
		"query": clip(content, 250), "wing": p.scope.wing(), "limit": 1,
	})
	if err != nil {
		return false, ""
	}
	var parsed struct {
		Results []struct {
			Text       string  `json:"text"`
			Similarity float64 `json:"similarity"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(out), &parsed) != nil || len(parsed.Results) == 0 {
		return false, ""
	}
	if parsed.Results[0].Similarity >= rememberDupThreshold {
		return true, parsed.Results[0].Text
	}
	return false, ""
}

type mpToolSpec struct {
	name     string
	desc     string
	params   string // JSON object properties (without outer braces)
	required []string
	execute  func(p *MempalaceProxy, ctx context.Context, args map[string]json.RawMessage) (string, error)
}

var mpCatalog = []mpToolSpec{
	{
		name: "recall_memory",
		desc: "Search the project's long-term memory (past decisions, prior work, learnings) semantically. Scoped to the current project automatically. Use this before asking a human or re-deriving a past decision.",
		params: `"query":{"type":"string","description":"Short search query — keywords or a question (max 250 chars)"},` +
			`"room":{"type":"string","description":"Optional room filter, e.g. 'decisions' or 'general'"},` +
			`"limit":{"type":"integer","description":"Max results (default 5, max 15)"}`,
		required: []string{"query"},
		execute: func(p *MempalaceProxy, ctx context.Context, args map[string]json.RawMessage) (string, error) {
			query := stringArg(args, "query")
			room := stringArg(args, "room")
			limit := intArg(args, "limit", 5, 15)
			call := map[string]any{"query": clip(query, 250), "wing": p.scope.wing(), "limit": limit}
			if room != "" {
				call["room"] = mempalace.SanitizeName(room)
			}
			out, err := p.call(ctx, "mempalace_search", call)
			p.record("recall_memory", "read", room, query, countResults(out))
			return out, err
		},
	},
	{
		name: "recall_run",
		desc: "Retrieve verbatim content from a specific run of the current task (defaults to the current run). Use when you need the exact wording of something from earlier work on this task.",
		params: `"query":{"type":"string","description":"What to look for (max 250 chars)"},` +
			`"run_id":{"type":"integer","description":"Run id to search within (default: current run)"}`,
		required: []string{"query"},
		execute: func(p *MempalaceProxy, ctx context.Context, args map[string]json.RawMessage) (string, error) {
			query := stringArg(args, "query")
			runID := int32(intArg(args, "run_id", int(p.scope.RunID), 1<<30))
			src := p.scope.sourceFile(runID)
			if src == "" {
				return "", fmt.Errorf("recall_run requires a task context")
			}
			call := map[string]any{"query": clip(query, 250), "wing": p.scope.wing(), "source_file": src, "limit": 8}
			out, err := p.call(ctx, "mempalace_search", call)
			p.record("recall_run", "read", "", query, countResults(out))
			return out, err
		},
	},
	{
		name: "remember",
		desc: "Store ONE important note, decision or learning in the project's long-term memory so future tasks and agents can recall it. Exactly one fact per call, max 500 characters — split multiple facts into separate calls; longer documents belong in write_artifact. Content is stored verbatim — write it self-contained.",
		params: `"content":{"type":"string","description":"One self-contained fact, verbatim, max 500 characters"},` +
			`"kind":{"type":"string","enum":["note","decision","learning"],"description":"What this is — decisions are filed in the decisions room"}`,
		required: []string{"content", "kind"},
		execute: func(p *MempalaceProxy, ctx context.Context, args map[string]json.RawMessage) (string, error) {
			content := stringArg(args, "content")
			kind := stringArg(args, "kind")
			if strings.TrimSpace(content) == "" {
				return "", fmt.Errorf("content is required")
			}
			// Cap keeps memories atomic (one fact) and always below the palace
			// chunk size, so a stored fact is never split across drawers.
			if len(content) > rememberMaxChars {
				return "", fmt.Errorf("content is %d chars, max %d — too long for a memory fact: split into separate remember calls (one fact each); documents belong in write_artifact", len(content), rememberMaxChars)
			}

			room := mempalace.RoomGeneral
			if kind == "decision" {
				room = mempalace.RoomDecisions
			}
			stored := content
			if p.scope.Closet != "" {
				stored = fmt.Sprintf("[%s] [%s] %s", p.scope.Closet, kind, content)
			} else {
				stored = fmt.Sprintf("[%s] %s", kind, content)
			}

			// Duplicate guard: keep the palace clean when agents re-store the
			// same fact across runs. Checked against the STORED form (prefix
			// included) — existing drawers carry the same prefix shape, and
			// like-vs-like comparison separates paraphrases from genuinely
			// different facts much better than raw-vs-prefixed. Returning the
			// existing text teaches the agent what memory already holds.
			if dup, existing := p.CheckDuplicate(ctx, stored); dup {
				p.record("remember", "write", "", clip(content, 120), 0)
				if existing != "" {
					return fmt.Sprintf("Already in memory (similar: %q) — not stored again.", clip(existing, 200)), nil
				}
				return "Already in memory (near-duplicate found) — not stored again.", nil
			}
			call := map[string]any{
				"wing":     p.scope.wing(),
				"room":     room,
				"content":  stored,
				"added_by": p.scope.AddedBy,
			}
			if src := p.scope.sourceFile(p.scope.RunID); src != "" {
				call["source_file"] = src
			}
			out, err := p.call(ctx, "mempalace_add_drawer", call)
			p.record("remember", "write", room, clip(content, 120), 1)
			return out, err
		},
	},
	{
		name: "memory_facts",
		desc: "Query the knowledge graph for structured facts (status, approach, blockers) about an entity (defaults to the current task). Entities are named task-<ref>, e.g. task-dec-62.",
		params: `"entity":{"type":"string","description":"Entity to query (default: the current task)"},` +
			`"as_of":{"type":"string","description":"Optional date filter YYYY-MM-DD — only facts valid at that time"}`,
		required: []string{},
		execute: func(p *MempalaceProxy, ctx context.Context, args map[string]json.RawMessage) (string, error) {
			entity := stringArg(args, "entity")
			if entity == "" {
				entity = p.scope.TaskEntity
			}
			if entity == "" {
				return "", fmt.Errorf("no entity given and no task context — pass an entity name")
			}
			call := map[string]any{"entity": entity}
			if asOf := stringArg(args, "as_of"); asOf != "" {
				call["as_of"] = asOf
			}
			out, err := p.call(ctx, "mempalace_kg_query", call)
			p.record("memory_facts", "read", "", entity, countFacts(out))
			return out, err
		},
	},
	// write_diary was removed from the agent tool surface: the engine's
	// run-teardown hook writes an auto-diary for every run (including failed
	// and canceled ones), which made the mandatory agent diary pure ceremony.
	// mempalace_diary_write is still called engine-side.
	{
		name: "memory_invalidate",
		desc: "Mark a knowledge-graph fact as no longer true (validity window closes; history is preserved). Use when the team changes direction — invalidate what you're overturning, then remember the new decision.",
		params: `"subject":{"type":"string","description":"Fact subject"},` +
			`"predicate":{"type":"string","description":"Fact predicate"},` +
			`"object":{"type":"string","description":"Fact object"},` +
			`"reason":{"type":"string","description":"Why this stopped being true"}`,
		required: []string{"subject", "predicate", "object"},
		execute: func(p *MempalaceProxy, ctx context.Context, args map[string]json.RawMessage) (string, error) {
			subject, predicate, object := stringArg(args, "subject"), stringArg(args, "predicate"), stringArg(args, "object")
			out, err := p.call(ctx, "mempalace_kg_invalidate", map[string]any{
				"subject": subject, "predicate": predicate, "object": object,
			})
			if err != nil {
				return "", err
			}
			// Superseding note in the decisions room keeps the "why" recallable.
			if reason := stringArg(args, "reason"); reason != "" {
				note := fmt.Sprintf("[superseded] %s %s %s — no longer true: %s", subject, predicate, object, reason)
				_, _ = p.call(ctx, "mempalace_add_drawer", map[string]any{
					"wing": p.scope.wing(), "room": mempalace.RoomDecisions,
					"content": note, "added_by": p.scope.AddedBy,
				})
			}
			p.record("memory_invalidate", "write", "", fmt.Sprintf("%s %s %s", subject, predicate, object), 1)
			return out, nil
		},
	},
	{
		name: "recall_company",
		desc: "Search memory across ALL projects of the company (cross-project recall). Use only for questions that genuinely span projects, e.g. 'have we hit this error anywhere before?'.",
		params: `"query":{"type":"string","description":"Short search query (max 250 chars)"},` +
			`"limit":{"type":"integer","description":"Max results (default 5, max 15)"}`,
		required: []string{"query"},
		execute: func(p *MempalaceProxy, ctx context.Context, args map[string]json.RawMessage) (string, error) {
			query := stringArg(args, "query")
			out, err := p.call(ctx, "mempalace_search", map[string]any{
				"query": clip(query, 250), "limit": intArg(args, "limit", 5, 15),
			})
			p.record("recall_company", "read", "", query, countResults(out))
			return out, err
		},
	},
}

// ---- mpProxyTool ----

type mpProxyTool struct {
	proxy *MempalaceProxy
	spec  mpToolSpec
}

func (t *mpProxyTool) Def() aicli.ToolDef {
	requiredJSON := "[]"
	if len(t.spec.required) > 0 {
		b, _ := json.Marshal(t.spec.required)
		requiredJSON = string(b)
	}
	params := fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s}`, t.spec.params, requiredJSON)
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        t.spec.name,
			Description: t.spec.desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

func (t *mpProxyTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var raw map[string]json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return "", err
		}
	}
	return t.spec.execute(t.proxy, ctx, raw)
}

// ---- helpers ----

func stringArg(args map[string]json.RawMessage, key string) string {
	var s string
	if v, ok := args[key]; ok {
		json.Unmarshal(v, &s)
	}
	return strings.TrimSpace(s)
}

func intArg(args map[string]json.RawMessage, key string, def, max int) int {
	var n int
	if v, ok := args[key]; ok {
		if err := json.Unmarshal(v, &n); err != nil || n <= 0 {
			return def
		}
		if n > max {
			return max
		}
		return n
	}
	return def
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// isDuplicate loosely parses a mempalace_check_duplicate response.
func isDuplicate(out string) bool {
	var parsed struct {
		Duplicate   *bool `json:"duplicate"`
		IsDuplicate *bool `json:"is_duplicate"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err == nil {
		if parsed.Duplicate != nil {
			return *parsed.Duplicate
		}
		if parsed.IsDuplicate != nil {
			return *parsed.IsDuplicate
		}
	}
	return strings.Contains(out, `"duplicate": true`) || strings.Contains(out, `"is_duplicate": true`)
}

// countResults extracts the result count from a mempalace_search response for
// activity stats; 0 on any parse miss.
func countResults(out string) int {
	var parsed struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err == nil {
		return len(parsed.Results)
	}
	return 0
}

func countFacts(out string) int {
	var parsed struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err == nil {
		return parsed.Count
	}
	return 0
}
