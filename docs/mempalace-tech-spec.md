# MemPalace Memory Layer — Tech Spec

**Status:** ready for implementation
**Rationale & research:** see `docs/mempalace-integration-plan.md` (taxonomy §3.1, invalidation §3.2, query model §3.3, scoping §3.4, export §3.5, integration depth §3.6)

- **Phase 1** — MemPalace integration (runtime, provisioning, agent tools, refinement hooks, diaries) + Memory UI
- **Phase 2** — Context compaction using MemPalace

---

# Shared foundations (Phase 1, used by both phases)

## F1. Runtime installation — `pkg/setup/mempalace.go`

Replace the dead npm stub (`integration/mempalace.go` — **delete it**; it points at the wrong package).

```go
package setup

const MempalaceVersion = "<pin latest at impl time>"

// EnsureMempalace installs the mempalace CLI + MCP server via uv (fallback pipx),
// pinned to MempalaceVersion. Idempotent; safe to run on every startup.
func EnsureMempalace(ctx context.Context) error
// MempalaceAvailable reports whether the mempalace binary is on PATH and healthy.
func MempalaceAvailable() bool   // runs `mempalace --version`, caches result
```

- Install command: `uv tool install "mempalace==<ver>"`; if `uv` absent, `pipx install`; if both absent → log warning, feature disabled.
- Called from the existing setup goroutine in `main.go` (next to `srv.InstallMCPNpmDeps`).
- **Feature flag:** everything memory-related checks `MempalaceAvailable()`. When false: no MCP server seeded, no tools registered, UI section shows an install-instructions empty state. The app must run exactly as today.

## F2. Palace provisioning — new package `pkg/mempalace/`

One palace per company at `<PaperclipHome()>/memory/<company.ShortName>/`.

```go
package mempalace

type Manager struct { /* queries *db.Queries, basePath string */ }

// EnsurePalace creates the palace dir + runs `mempalace init --dir <projectWorkspace>
// --backend chroma --yes --no-llm` on first use, and seeds/updates the company's
// MCPServer row. Mirrors codegraph's InitStatus lifecycle.
func (m *Manager) EnsurePalace(ctx context.Context, company db.Company) error

// PalacePath returns <PaperclipHome>/memory/<shortName>.
func (m *Manager) PalacePath(shortName string) string

// MineProject runs `mempalace mine --dir <workspace> --wing project:<name>` in a
// background goroutine, updating MCPServer.InitStatus ("initializing" → "ready" / "error: ...").
func (m *Manager) MineProject(ctx context.Context, company db.Company, project db.Project) error
```

**MCPServer seeding** (in `db/queries_mcp.go` `EnsureBuiltinMCPServers`, one row per company):

| Field | Value |
|---|---|
| Name | `mempalace-<shortname>` (unique slug) |
| Transport | `stdio` |
| Command | `mempalace-mcp` |
| Args | `--palace <PalacePath>` |
| Builtin | `true`, Enabled `true` |
| InitStatus | codegraph-style lifecycle |

Startup sweep (like codegraph's in `main.go:153`): re-kick palaces stuck in `initializing`.

**Hook points:** company creation → `EnsurePalace`; project creation (`server/controllers/projects.go`, next to `startCodegraphInit`) → `MineProject`.

## F3. Memory addressing — `pkg/mempalace/addressing.go`

Single source of truth for taxonomy. **No other code composes wing/room/closet/source_file strings.**

```go
type Address struct {
    Wing       string // "project:<project.Name>" | "agent:<agent.Name>" | "company"
    Room       string // "sprint:<sprint.Name>" | "architecture" | "decisions" | "requirements"
    Closet     string // "task-<task.DisplayID>"  (STABLE display id, never DB auto-increment)
    SourceFile string // "runs/<task-display-id>/<run-id>/<session-id>.jsonl"
    AddedBy    string // agent name
}

// Resolve builds the Address for the current execution context.
func Resolve(company db.Company, project *db.Project, sprint *db.Sprint,
             task *db.Task, run *db.Run, agent *db.Agent) Address
```

Rules (from plan §3.1/§3.5):
- Keys are **stable names** (shortname, project name, task display ID) — never DB IDs (restore reassigns them).
- Normalization (lowercase, spaces→`-`) happens here only.
- Per-company **write mutex** lives here too: `func (m *Manager) LockCompany(shortName string) func()` — held around every write tool call and around backup.

## F4. Go-side MCP access — reuse `engine/mcp`

The engine, API layer, and proxy all talk to the palace through the existing `mcp.NewClient(server db.MCPServer)` stdio client. No new transport code. Maintenance ops (mine/sweep/compress/sync) shell out to the CLI via `exec.Command` (pattern: codegraph init).

---

# Phase 1 — MemPalace integration + Memory UI

## 1.1 Agent tools — `engine/aicli/tools/mempalace.go` (`MempalaceProxy`)

Clone the `CodegraphProxy` structure (`codegraph.go`): lazy per-run MCP client, `RegisterAll(ctx, registry) string`, `Close()`. Curated tools with **engine-injected scope** — the agent never passes wing/closet:

| Tool | Params (agent-visible) | Maps to | Injected |
|---|---|---|---|
| `recall_memory` | `query` (req), `room?`, `limit?` (def 5, max 15) | `mempalace_search` | `wing` = current project wing |
| `recall_run` | `query` (req), `run_id?` (def: current run) | `mempalace_search` | `wing` + `source_file` from Address |
| `remember` | `content` (req), `kind` (enum: `note`,`decision`,`learning`) | `mempalace_check_duplicate` → `mempalace_add_drawer` (skip if dup ≥ 0.95) | full Address; `kind=decision` also targets `decisions` room |
| `memory_facts` | `entity?`, `as_of?` | `mempalace_kg_*` query/timeline | entity default = current task |
| `write_diary` | `what_happened`, `learned`, `what_matters` | `mempalace_diary_write` | `wing` = `agent:<name>` |

Planner-tier only (CEO/CTO — via `AllowedTools`): `memory_invalidate(subject, predicate, object, reason)` → `kg_invalidate` + supersede note; `recall_company(query)` → unscoped/`company`-wing search.

- Tool-call timeout 2 min (same as codegraph); write tools take the company write lock.
- Registration in `executeSession` (`native_engine.go`, next to `NewCodegraphProxy` at `:740`), gated on `MempalaceAvailable()` + palace `InitStatus == "ready"`.
- **Default `AllowedTools`** (`engine/agentconfig/defaults.go`): all roles get `recall_memory`, `recall_run`, `memory_facts`, `write_diary`; `remember` for all; `memory_invalidate`/`recall_company` only CEO/CTO.
- Raw `call_mcp_tool` access to the mempalace server: **disabled by default** for all agents via `AgentMCPToolFilter` seeding; can be re-enabled per agent in the UI.

## 1.2 System prompt — Palace Protocol block

In `engine/system_prompt.go`, when memory is ready, append a section:

```
## Memory
Project wing: project:<name>. Current task closet: task-<display-id>.
<wake-up output, cached>
Protocol: before asking a human or re-deriving a past decision, call recall_memory.
Treat memory_facts (knowledge graph) as current truth; recalled drawer text is
historical record — on conflict, facts win. Before finish_task, call write_diary.
```

- Wake-up content: `mempalace wake-up --wing project:<name>` via CLI, cached per project for 1h, hard-capped at 1200 tokens (truncate).
- Replace `engine/memory.go` `initTaskMemory` (static `memory.md`) with a wake-up snapshot written to the workspace; keep the function name/callsite (`native_engine.go:455`).

## 1.3 Refinement integration

In `NativeEngine.buildInitialMessages` (`native_engine.go:930`), when `mode == "plan"` and memory ready:
- Run `mempalace_search` (Go client, not agent tool): query = task title + first 500 chars of description, `wing` = project wing, limit 5.
- Append to seed message: `"## Possibly relevant prior work/decisions\n<results with dates + rooms>"`, capped 2k tokens.

After a plan-mode run completes (where `RefinedDescription` is persisted):
- Store drawer: refined description + acceptance criteria → Address closet, `room=decisions` if it contains decisions; `added_by = <agent>`; dedupe-check first.
- If a previous plan drawer exists for this closet (query by closet + kind metadata): prepend `[SUPERSEDED by run <id>]` to it via `mempalace_update_drawer`, and swap KG facts (`kg_invalidate` old approach fact, `kg_add` new: `task-<id> --approach--> <summary>`; `--supersedes--> <old>`).

## 1.4 Diary on task completion

In the `finish_task` execution path (`engine/aicli/tools/finish_task_execution.go`): if the agent has not called `write_diary` during the session, the engine writes a minimal auto-diary (task, status, result summary) to `agent:<name>` wing. Non-blocking (goroutine, logged on failure).

## 1.5 Activity logging — `db.MemoryActivity`

```go
// MemoryActivity records every memory operation for the UI activity feed.
type MemoryActivity struct {
    ID        int32     `json:"id" gorm:"primaryKey"`
    CompanyID int32     `json:"company_id" gorm:"index"`
    AgentName string    `json:"agent_name" gorm:"index"` // "" = engine/UI-initiated
    TaskID    *int32    `json:"task_id" gorm:"index"`
    RunID     *int32    `json:"run_id"`
    Tool      string    `json:"tool"`               // recall_memory, remember, api:search, ...
    Kind      string    `json:"kind" gorm:"index"`  // "read" | "write" | "maintenance"
    Wing      string    `json:"wing"`
    Room      string    `json:"room"`
    Query     string    `json:"query" gorm:"type:text"`   // truncated to 500 chars
    ResultN   int       `json:"result_n"`
    CreatedAt time.Time `json:"created_at" gorm:"index"`
}
```

Add to AutoMigrate list (`main.go:77`). Written by: MempalaceProxy tool executions, engine hooks (refinement/wake-up), `/api/memory` handlers. Retention: purge rows older than 90 days in the daily scheduler.

## 1.6 API — `server/controllers/memory.go`, mounted at `/api/companies/{shortName}/memory`

All handlers: resolve company → its mempalace MCPServer row → shared Go MCP client (one cached client per company in the Manager, not per request). 503 + explanatory JSON when memory unavailable.

| Method & path | Backed by | Notes |
|---|---|---|
| `GET /status` | `mempalace_status` + InitStatus | drawer counts, health |
| `GET /taxonomy` | `mempalace_get_taxonomy` | wing→room→count tree |
| `GET /search?q&wing&room&limit` | `mempalace_search` | UI search tab |
| `GET /graph` | `graph_stats` + `list_tunnels` + `list_hallways` + `list_rooms` | nodes+edges JSON for React Flow: `{nodes:[{id,label,kind,drawerCount}], edges:[{from,to,kind,label}]}` |
| `GET /drawers?wing&room&closet&page` | scoped search w/ empty-ish query + metadata | drawer browser |
| `PUT /drawers/{id}` | `mempalace_update_drawer` | corrections; body `{content}` |
| `DELETE /drawers/{id}` | `mempalace_delete_drawer` | confirm-guarded in UI |
| `POST /drawers/{id}/supersede` | `update_drawer` (mark) + optional `kg_invalidate` | body `{reason}` |
| `GET /facts?entity&as_of` / `GET /facts/timeline?entity` | KG query tools | facts tab |
| `POST /facts` / `POST /facts/invalidate` | `kg_add` / `kg_invalidate` | manual corrections |
| `GET /activity?agent&kind&page` | `MemoryActivity` table | feed + stats aggregates |
| `GET /agents` | diaries + `added_by` aggregation over activity | per-agent view |
| `POST /maintenance/{mine\|sync\|compress}` | CLI via Manager, async | returns job status; guarded by write lock |

Mount in `server/handlers.go` next to the mcp-servers router. All writes also insert `MemoryActivity` rows.

## 1.7 Frontend — Memory section

- `frontend/src/components/Sidebar.tsx`: add `{ icon: Brain, label: 'Memory', path: `${base}/memory` }` (lucide `Brain`).
- `frontend/src/App.tsx`: route `/companies/:shortName/memory` → `pages/Memory.tsx`.
- **New dependency:** `@xyflow/react` (React Flow, MIT) for the graph tab. No other new deps.
- `pages/Memory.tsx` — tab layout (pattern: existing pages):
  1. **Explorer** — left: taxonomy tree (wings→rooms→closets, counts); right: drawer list + content viewer with Edit / Delete / Mark-superseded actions (`react-markdown` for content).
  2. **Graph** — React Flow canvas from `GET /graph`; node click → side panel with room's drawers; distinct edge styles for tunnels vs hallways.
  3. **Search** — query box + wing/room selects + similarity scores; row click → drawer viewer.
  4. **Facts** — KG table (subject/predicate/object/valid range), "as of" date picker, timeline per entity, invalidate button (planner confirmation modal).
  5. **Activity & Stats** — feed from `/activity`; stat tiles (drawers total, writes this week, top agents, top wings) from `/status` + activity aggregates.
  6. **Agents** — per-agent cards: diary entries, drawers-written count, last activity; link to agent's tool-access settings (existing AgentDetails page).
- Empty/degraded states: not installed → install instructions; `initializing` → progress note (poll `/status`).

## 1.8 Backup integration (minimal slice)

In `pkg/backup/backup.go` `CreateBackup`: `addDirectoryToTar(tw, filepath.Join(basePath, "memory"), "memory", &itemCount)` while holding all company write locks. In `restore.go` `RestoreBackup`: restore `memory/`, then per company run `mempalace repair --mode from-sqlite` + `mempalace sync --dry-run` (log-only). (Full logical export deferred — plan §3.5.)

## 1.9 Phase 1 acceptance criteria

1. Fresh install without Python/uv: app runs unchanged; Memory UI shows install guidance; no errors in agent runs.
2. With mempalace installed: creating a company provisions a palace; creating a project mines it in background; `GET /status` reaches `ready`.
3. An agent in a task run can `recall_memory` and gets project-scoped results; `remember` files a drawer visible in the Explorer tab under the right wing/room/closet.
4. Two agents writing concurrently (parallel runs) produce no palace corruption (write-lock test).
5. Plan-mode run injects "prior work" section when relevant memories exist; refined output appears as a drawer; re-planning marks the old plan superseded and updates KG facts.
6. UI: all six tabs functional against a seeded palace; drawer edit/delete/supersede round-trips; graph renders ≥50 nodes without jank.
7. Activity feed shows agent recalls/writes with correct attribution.
8. Backup → wipe `memory/` → restore → search returns identical results (repair path verified).
9. All memory features no-op gracefully mid-run if the MCP server dies (tool returns error string, run continues).

---

# Phase 2 — Context compaction using MemPalace

Design (plan §4.3): compaction = **externalize verbatim + selectively re-inject**, not summarize. MemPalace stores verbatim; relevance selection happens at recall time.

## 2.1 Run transcripts — `engine/aicli/transcript.go`

```go
// TranscriptWriter appends one JSON line per message to the run's transcript.
type TranscriptWriter struct { /* file handle, path, mu */ }
func NewTranscriptWriter(path string) (*TranscriptWriter, error)  // path from Address.SourceFile under PaperclipHome
func (w *TranscriptWriter) Append(msg aicli.Message, turn int) error
```

- Wired into `runMessageHistory` (`engine/aicli/agent.go`): every appended history message is also written to the transcript. Flush per message (crash-safe).
- Line format matches what `mempalace sweep` consumes (role, content, timestamp, session id).

## 2.2 Incremental offload

Background filer in the Manager: after each turn (or every K=5 messages), run `mempalace sweep <transcript>` (idempotent, message-level, resume-safe — designed for exactly this). Runs under the company write lock, in a goroutine serialized per run; never blocks the agent loop. At session end (`executeSession` cleanup), one final sweep guarantees completeness.

## 2.3 Compactor — replaces/extends `pruneHistory` (`engine/aicli/agent.go:455`)

```go
type CompactionConfig struct {
    Enabled          bool
    TriggerTokens    int // default: 70% of model context window
    KeepFreshTurns   int // default 6 assistant turns (reuse freshAssistantTurns)
    BridgeMaxTokens  int // default 2500
    DigestResults    int // default 6
}

// Compactor builds the memory bridge. Injected into the agent loop by the engine
// (aicli package stays dependency-free of db/mempalace via this interface).
type Compactor interface {
    // BuildBridge returns the synthetic bridge message replacing dropped history.
    BuildBridge(ctx context.Context, dropped []Message, recent []Message) (Message, error)
}
```

Loop changes in `runMessageHistory`:
1. Estimate history tokens each turn (existing `pkg/tokens` counter).
2. If `> TriggerTokens` and `Compactor != nil`: split history into `[system] + [bridge?] + old + fresh(KeepFreshTurns)`; **cut only at assistant-turn boundaries, never between a tool_call and its tool result**.
3. Replace `old` (and any previous bridge) with the new bridge message (role `user`, clearly delimited). Bridge is **rebuilt, not appended** on every compaction.
4. Fallback: if bridge construction fails, fall back to today's `pruneHistory` truncation (never fail the run because memory hiccuped).

## 2.4 Bridge message content (engine-side implementation of `Compactor`, `engine/compactor.go`)

Ordered sections, total ≤ `BridgeMaxTokens`:
1. **Pinned (from DB, never recall-dependent):** task title, refined description, acceptance criteria, mode — ~400 tokens.
2. **Current facts (complete):** valid KG facts for the task entity (`memory_facts` equivalent via Go client) — decisions, approach, state.
3. **Recall digest (adaptive):** `mempalace_search` scoped to the task closet; query = task title + last 2 user/assistant messages; top `DigestResults`, each entry: `[date, room] excerpt…` — trimmed to fit budget.
4. **Self-serve instruction:** `"<N> earlier messages moved to memory. recall_run(query) retrieves any of them verbatim."`
5. Counter of dropped messages + ids of dropped tool calls (audit line).

Precondition: dropped messages are confirmed swept (2.2's final-state check) before removal — a message is never dropped from context unless it is already in the palace.

## 2.5 Seed slimming (cross-run)

`buildInitialMessages` (`native_engine.go:930`) currently replays ALL past run results + comments. When memory is ready and history exceeds ~4k tokens: keep the last 2 runs + last 10 comments verbatim; replace older ones with a recall digest (same builder, seeded with task description). Feature-flagged separately (`CompactSeed bool`).

## 2.6 Configuration

App settings (YAML + `/api/settings` + Settings page): `memory.compaction.enabled` (default **false** on rollout, flip after bake-in), `trigger_ratio` (0.7), `keep_fresh_turns` (6), `bridge_max_tokens` (2500). Per-model context-window sizes come from the existing provider/model config.

## 2.7 Optional (iteration 2, behind its own flag): rolling summary hybrid

On each compaction, one `UtilityModel` call produces a ≤10-line session narrative → prepended to bridge section 1 and stored as a drawer (`kind=summary`, recallable). Ship only after the pure recall bridge is validated; adds latency/cost per compaction.

## 2.8 Phase 2 acceptance criteria

1. **Fact-recovery e2e (the core property):** scripted long session plants a unique fact in message ~5, forces compaction (low `TriggerTokens`), then asks for the fact → agent answers correctly (from digest or via `recall_run`).
2. Tool-call/result pairs are never split by the cut (unit test on boundary selection with synthetic histories).
3. A message is never dropped before its sweep is confirmed (unit test: sweep failure → compaction deferred, old `pruneHistory` used).
4. Bridge stays ≤ `BridgeMaxTokens`; repeated compactions don't accumulate multiple bridges (exactly one present).
5. Token usage on a 100-turn synthetic run is reduced ≥40% vs. no-compaction baseline while task completion (e2e suite) stays green.
6. Compaction disabled or mempalace dead → behavior byte-identical to current `pruneHistory` path.
7. Transcript + sweep add <50ms p95 overhead per turn (measured; filing is async).

---

# Rollout & sequencing

1. **P1 wave 1 (foundations):** F1–F4, 1.1, 1.2, 1.5 — agents get memory tools. *(~1 wk)*
2. **P1 wave 2:** 1.3, 1.4, 1.6, 1.7, 1.8 — refinement hooks + full UI + backup. *(~1.5 wk)*
3. **P2:** 2.1–2.6 (2.7 later). Compaction ships default-off, enabled per company in settings, flipped to default-on after the e2e suite + one week of dogfooding. *(~1 wk)*

Risks tracked from plan §5: Python runtime (F1 flag), embedding download (background init), write concurrency (F3 lock + daemon), version drift (pinned version + `repair` on restore).
