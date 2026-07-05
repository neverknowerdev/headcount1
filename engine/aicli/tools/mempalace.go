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
// args/response are the full command and mempalace's raw reply, shown by the
// Activity tab's "view full log" detail; pass "" when not cheaply available
// (most engine hooks — their query field already carries a preview).
type MemoryActivityFunc func(tool, kind, wing, room, query string, resultN int, args, response string)

// MempalaceProxy exposes curated memory tools backed by the company's
// mempalace MCP server (codegraph-proxy pattern: register on the run's
// registry, engine injects all scoping). The underlying stdio client is the
// package-level cached one in pkg/mempalace, shared with the engine hooks and
// the /api/memory handlers so all palace writes serialize on one lock.
type MempalaceProxy struct {
	server        db.MCPServer
	scope         MemoryScope
	onActivity    MemoryActivityFunc
	diaryWritten  atomic.Bool
	rememberCount atomic.Int32
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

// HasRemembered reports whether remember() stored at least one new fact this
// run (near-duplicate rejections don't count — see the remember tool). Used
// by finish_task's one-time nudge: an agent that never stored anything is
// asked once to capture durable facts/learnings before the run's institutional
// knowledge is lost, mirroring MemPalace's own Claude Code stop-hook pattern
// (mechanical nudge, not a hard requirement — teardown capture is the real
// backstop for agents that ignore it or crash).
func (p *MempalaceProxy) HasRemembered() bool { return p.rememberCount.Load() > 0 }

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

// record logs one memory operation for the activity feed. args is the raw
// call the agent made (marshaled to JSON for the detail view — never nil,
// pass map[string]any{} if there's nothing to show); response is mempalace's
// raw reply. Both are truncated on write (see CreateMemoryActivity) so it's
// fine to pass them in full here.
func (p *MempalaceProxy) record(tool, kind, room, query string, resultN int, args map[string]any, response string) {
	if p.onActivity != nil {
		argsJSON := ""
		if len(args) > 0 {
			if b, err := json.Marshal(args); err == nil {
				argsJSON = string(b)
			}
		}
		p.onActivity(tool, kind, p.scope.wing(), room, query, resultN, argsJSON, response)
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
// same fact ("no agent has shell tools" stored 7×).
//
// Dedup is checked (and drawers are stored) as RAW content — no "[closet]
// [kind]" prefix. Measured with the shipped MiniLM embeddings, adding any
// shared boilerplate tag to both sides compresses the similarity range and
// costs margin in both directions (e.g. a same-topic pair scores 0.83 raw vs
// 0.54 with a shared prefix). The prefix also turned out to actively harm
// safety: two agents filing the SAME fact under different `kind` tags
// ("learning" vs "note") dropped a genuine duplicate from 0.83 to 0.75 raw —
// a mismatched tag alone can push a real duplicate below any threshold set
// to protect decision reversals. Raw content has no such failure mode.
//
// Threshold picked from measured raw-content similarity:
//   - paraphrases of the same fact (reordered clauses, synonyms, prefix/
//     task-ref noise): 0.856–0.944
//   - a genuine decision reversal on the same topic ("use WebSockets" →
//     "use SSE, WebSockets rejected") — must NOT be treated as a duplicate,
//     or overturning a decision would silently fail to record: 0.754
//   - genuinely different facts sharing vocabulary ("lack shell tools" vs
//     "lack browser tools"): 0.040–0.399
//
// 0.80 sits with ~0.05 margin on both sides of the (0.754, 0.856) safe
// window. Tune with the paraphrase-gauntlet e2e test if the model changes.
const rememberDupThreshold = 0.80

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
			// Over-fetch and re-rank locally (see mempalace.RerankSearchResults): mempalace's
			// own similarity ranking has no notion of "this is boilerplate" or
			// "this was superseded" — a bare task-description echo or a raw
			// transcript dump can outrank the actual fact for a status-style
			// query. Explicit room filters bypass this (the caller already
			// narrowed scope), so only rerank the unscoped/default case.
			fetchLimit := limit
			if room == "" {
				fetchLimit = limit * 3
				if fetchLimit > 30 {
					fetchLimit = 30
				}
			}
			call := map[string]any{"query": clip(query, 250), "wing": p.scope.wing(), "limit": fetchLimit}
			if room != "" {
				call["room"] = mempalace.SanitizeName(room)
			}
			out, err := p.call(ctx, "mempalace_search", call)
			if err == nil && room == "" {
				out = mempalace.RerankSearchResults(out, limit)
			}
			p.record("recall_memory", "read", room, query, countResults(out), call, out)
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
			p.record("recall_run", "read", "", query, countResults(out), call, out)
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

			// Duplicate guard: keep the palace clean when agents re-store the
			// same fact across runs. Checked (and stored) as RAW content — no
			// "[closet] [kind]" tag — see rememberDupThreshold for why: a
			// shared prefix compresses the embedding's similarity range and a
			// mismatched kind tag can drag a genuine duplicate low enough to
			// evade any threshold that also has to protect decision
			// reversals. The task association isn't lost — every drawer here
			// carries source_file ("tasks/<ref>/run-N"), which mempalace
			// search results echo back. Returning the existing text teaches
			// the agent what memory already holds.
			if dup, existing := p.CheckDuplicate(ctx, content); dup {
				p.record("remember", "write", "", clip(content, 120), 0,
					map[string]any{"content": content, "kind": kind}, "duplicate — not stored: "+existing)
				if existing != "" {
					return fmt.Sprintf("Already in memory (similar: %q) — not stored again.", clip(existing, 200)), nil
				}
				return "Already in memory (near-duplicate found) — not stored again.", nil
			}
			call := map[string]any{
				"wing":     p.scope.wing(),
				"room":     room,
				"content":  content,
				"added_by": p.scope.AddedBy,
			}
			if src := p.scope.sourceFile(p.scope.RunID); src != "" {
				call["source_file"] = src
			}
			out, err := p.call(ctx, "mempalace_add_drawer", call)
			if err == nil {
				p.rememberCount.Add(1)
			}
			p.record("remember", "write", room, clip(content, 120), 1, call, out)
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
			p.record("memory_facts", "read", "", entity, countFacts(out), call, out)
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

			// Honesty check: mempalace_kg_invalidate reports "success" even
			// for a (subject, predicate, object) that was never actually
			// added as a KG fact — agents can't add arbitrary KG facts
			// themselves (only engine hooks write the KG), so most facts an
			// agent tries to invalidate here don't exist as KG triples at
			// all. Blindly reporting success gave false confidence that the
			// invalidation had a real effect on future recall, when it
			// couldn't have. Check first; if nothing matches, say so.
			factExists := false
			if kgOut, kgErr := p.call(ctx, "mempalace_kg_query", map[string]any{"entity": subject}); kgErr == nil {
				var parsed struct {
					Facts []struct {
						Predicate string `json:"predicate"`
						Object    string `json:"object"`
						Current   bool   `json:"current"`
					} `json:"facts"`
				}
				if json.Unmarshal([]byte(kgOut), &parsed) == nil {
					for _, f := range parsed.Facts {
						if f.Current && f.Predicate == predicate && f.Object == object {
							factExists = true
							break
						}
					}
				}
			}

			if factExists {
				if _, err := p.call(ctx, "mempalace_kg_invalidate", map[string]any{
					"subject": subject, "predicate": predicate, "object": object,
				}); err != nil {
					return "", err
				}
			}

			// Mark the actual drawer(s) being overturned, not just a new
			// companion note — otherwise the substantive stale content (e.g.
			// the original decision text) never gets demoted at recall time
			// and can keep outranking the new, current decision. Scoped to
			// general + decisions (where remember() files notes/decisions),
			// substring-matched on the object (mempalace_search doesn't
			// return drawer_id, so list_drawers + prefix match is the
			// available mechanism — same pattern as supersedePreviousPlan).
			markedCount := 0
			if object != "" {
				for _, room := range []string{mempalace.RoomDecisions, mempalace.RoomGeneral} {
					if markedCount >= 3 {
						break
					}
					markedCount += markMatchingDrawersSuperseded(ctx, p, room, object, 3-markedCount)
				}
			}

			// Superseding note in the decisions room keeps the "why" recallable.
			if reason := stringArg(args, "reason"); reason != "" {
				note := fmt.Sprintf("[superseded] %s %s %s — no longer true: %s", subject, predicate, object, reason)
				_, _ = p.call(ctx, "mempalace_add_drawer", map[string]any{
					"wing": p.scope.wing(), "room": mempalace.RoomDecisions,
					"content": note, "added_by": p.scope.AddedBy,
				})
			}
			invalidateArgs := map[string]any{"subject": subject, "predicate": predicate, "object": object, "reason": stringArg(args, "reason")}
			invalidateResp := fmt.Sprintf("fact_existed=%v, drawers_marked_superseded=%d", factExists, markedCount)
			p.record("memory_invalidate", "write", "", fmt.Sprintf("%s %s %s", subject, predicate, object), 1, invalidateArgs, invalidateResp)

			if !factExists {
				return fmt.Sprintf("No current KG fact found for %s %s %s — nothing to invalidate in the knowledge graph. "+
					"Marked %d matching drawer(s) as superseded and recorded a note; "+
					"if you meant to overturn an earlier written decision, this is that.", subject, predicate, object, markedCount), nil
			}
			return fmt.Sprintf(`{"success":true,"fact":"%s %s %s","drawers_marked_superseded":%d}`, subject, predicate, object, markedCount), nil
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
			limit := intArg(args, "limit", 5, 15)
			fetchLimit := limit * 3
			if fetchLimit > 30 {
				fetchLimit = 30
			}
			companyCall := map[string]any{"query": clip(query, 250), "limit": fetchLimit}
			out, err := p.call(ctx, "mempalace_search", companyCall)
			if err == nil {
				out = mempalace.RerankSearchResults(out, limit)
			}
			p.record("recall_company", "read", "", query, countResults(out), companyCall, out)
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

// markMatchingDrawersSuperseded prefixes drawers in the given room whose
// preview contains needle (case-insensitive substring — mempalace_search
// doesn't return drawer_id, so semantic matching isn't available here; this
// mirrors the engine's existing supersedePreviousPlan pattern: list, filter,
// fetch full content, update) with a [SUPERSEDED] marker, so
// mempalace.RerankSearchResults demotes the real overturned content at recall time —
// not just the small companion note memory_invalidate also writes. Returns
// the number of drawers marked, capped at maxMark.
func markMatchingDrawersSuperseded(ctx context.Context, p *MempalaceProxy, room, needle string, maxMark int) int {
	if maxMark <= 0 {
		return 0
	}
	out, err := p.call(ctx, "mempalace_list_drawers", map[string]any{
		"wing": p.scope.wing(), "room": room, "limit": 100,
	})
	if err != nil {
		return 0
	}
	var parsed struct {
		Drawers []struct {
			DrawerID string `json:"drawer_id"`
			Preview  string `json:"content_preview"`
		} `json:"drawers"`
	}
	if json.Unmarshal([]byte(out), &parsed) != nil {
		return 0
	}
	needleLower := strings.ToLower(needle)
	marked := 0
	for _, d := range parsed.Drawers {
		if marked >= maxMark {
			break
		}
		if strings.HasPrefix(d.Preview, "[SUPERSEDED") || strings.HasPrefix(d.Preview, "[superseded]") {
			continue
		}
		if !strings.Contains(strings.ToLower(d.Preview), needleLower) {
			continue
		}
		full, fErr := p.call(ctx, "mempalace_get_drawer", map[string]any{"drawer_id": d.DrawerID})
		if fErr != nil {
			continue
		}
		var drawer struct {
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(full), &drawer) != nil || drawer.Content == "" {
			continue
		}
		if strings.HasPrefix(drawer.Content, "[SUPERSEDED") || strings.HasPrefix(drawer.Content, "[superseded]") {
			continue
		}
		if _, uErr := p.call(ctx, "mempalace_update_drawer", map[string]any{
			"drawer_id": d.DrawerID,
			"content":   fmt.Sprintf("[SUPERSEDED] %s", drawer.Content),
		}); uErr == nil {
			marked++
		}
	}
	return marked
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
