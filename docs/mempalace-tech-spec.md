# MemPalace Memory Layer — Tech Spec

**Status:** ready for implementation
**Rationale & research:** see `docs/mempalace-integration-plan.md` (taxonomy §3.1, invalidation §3.2, query model §3.3, scoping §3.4, export §3.5, integration depth §3.6)

- **Phase 1** — MemPalace integration (runtime, provisioning, agent tools, refinement hooks, diaries) + Memory UI
- **Phase 2** — Context compaction using MemPalace

---

# Shared foundations (Phase 1, used by both phases)

## F1. Runtime installation — extend the existing setup-script infra

**Supersedes any bespoke Go installer.** `main` (merged in) landed a mature cross-platform dependency-install pipeline: `pkg/setup/setup.go` (`Run`/`Status`/`Failures`/`Warnings`, blocking vs soft failures, `[setup] DETAIL_BEGIN/END` parsing) driving embedded scripts `pkg/setup/scripts/{setup-linux.sh, setup-macos.sh, setup-windows.ps1}`. It already installs `python3`, creates an isolated venv for `markitdown` (`$HOME/.paperclip2/venv`, `PAPERCLIP_VENV_DIR` override — sidesteps PEP 668), and installs `codegraph` via npm as a **blocking** dependency. Mempalace is a **new block appended to all three scripts**, modeled on those two patterns. Delete the dead npm stub (`integration/mempalace.go` — wrong package, never called).

**Install strategy — reuse the markitdown venv, don't add a second one:**

```sh
# ── mempalace ────────────────────────────────────────────────────────────────
# Installed into the same isolated venv as markitdown (see above) so it never
# fights PEP 668 / a Homebrew-managed system python3. Soft failure: memory is
# an optional feature, absence must never block app startup.
if [ -x "$VENV_DIR/bin/mempalace-mcp" ]; then
    echo "[setup] mempalace: OK"
elif [ -x "$VENV_DIR/bin/python3" ]; then
    echo "[setup] mempalace: not found — installing..."
    install_output=$("$VENV_DIR/bin/python3" -m pip install "mempalace==<PINNED_VERSION>" 2>&1)
    if [ -x "$VENV_DIR/bin/mempalace-mcp" ]; then
        echo "[setup] mempalace: installed"
    else
        add_soft_failure "mempalace" "could not be installed — memory features will be unavailable" "$install_output"
    fi
else
    add_soft_failure "mempalace" "venv unavailable (see markitdown failure above) — memory features will be unavailable" ""
fi
```

- **Version pin:** a single `<PINNED_VERSION>` constant substituted identically into all three scripts (shell vars in `.sh`, `$MempalacePinnedVersion` in `.ps1`) — keep it in one place (e.g. a generated header or a value `pkg/setup/setup.go` writes into the temp script alongside the existing `PAPERCLIP_VENV_DIR` env var, so bumping the version doesn't require editing three files by hand).
- **Windows parity** (`setup-windows.ps1`): same idea — `& $pythonCmd -m pip install "mempalace==<ver>"`, binary check via `Test-Command`/venv `Scripts\mempalace-mcp.exe`, `Add-SoftFailure` (mirroring `Add-Failure`, the same way `gh CLI` on Linux is soft while `codegraph` is hard).
- **Blocking vs soft:** unlike `codegraph` (hard failure — code intelligence is core), mempalace is registered via `add_soft_failure` (the `gh CLI` pattern) — the app must start normally without it; Memory UI/tools degrade gracefully instead.
- **Go-side exposure** — extend `pkg/setup/setup.go`, no new package:
  ```go
  // MempalaceAvailable reports whether the setup script found/installed
  // mempalace-mcp, independent of other (unrelated) setup failures.
  func MempalaceAvailable() bool   // same pattern as ready/MarkitdownAvailable, its own atomic.Bool
  // MempalacePath returns the venv-scoped mempalace-mcp binary path (mirrors PythonInterpreter()).
  func MempalacePath() string      // filepath.Join(venvDir(), "bin", "mempalace-mcp") (.exe on Windows)
  ```
  Determined the same way `ready`/markitdown is: scan `runOnce`'s captured output for `"[setup] mempalace: OK"` / `"[setup] mempalace: installed"`.
- **Feature flag:** everything memory-related checks `setup.MempalaceAvailable()`. When false: no MCP server seeded, no tools registered, UI section shows an install-instructions empty state (surfacing `setup.Warnings()` if mempalace appears there). The app must run exactly as today.
- `SetupGate.tsx` (frontend, from the merged setup-script PR) already renders blocking vs warning failures from `/api/settings`-adjacent status; extend its warnings list to include `mempalace` the same way `gh CLI` shows up today — no new UI plumbing needed for the setup gate itself (separate from the Memory section's own empty state in §1.7).

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
- **Scope note — two context layers, don't conflate them.** Wake-up is wing-granular only (no `--room`/`--closet` flag) and emits L0/L1 *identity + essential story*: slowly-changing, query-independent → cacheable, lives in the system prompt. Task-level context is the opposite (fast-moving, query-dependent) and is NOT a wake-up: it's the **task briefing** — pinned DB facts + valid KG facts + closet-scoped search with a current-state query. One shared builder serves all three task-briefing call sites so they can't drift apart:
  ```go
  // engine/briefing.go — used by refinement injection (§1.3), seed slimming (§2.5),
  // and bridge sections 1–3 (§2.4). querySeed: task description at run start;
  // recent messages at compaction time.
  func BuildTaskBriefing(ctx context.Context, task *db.Task, querySeed string, maxTokens int) (string, error)
  ```
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

> **Revised by Phase 1.5 (§1.5.1):** the pilot showed this trigger never fires where it matters — failed/canceled/hung runs never call `finish_task`. Auto-capture moves to run teardown on *any* terminal status.

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

# Phase 1.5 — Guaranteed capture & retrieval quality (post-pilot revision)

**Source:** analysis of the first real multi-agent batch (DEC-59–65, runs 80–95, 2026-07-04; 62 `memory_activities` rows) plus a review of upstream MemPalace's own harness integrations (`hooks/` for Claude Code, Codex CLI, Cursor — see plan §7).

**Pilot verdict in one line:** the pull side works (recall-first discipline held, cross-run knowledge transfer happened), the push side is fragile — capture depends entirely on agent cooperation and a clean `finish_task`, and retrieval quality is degraded by 800-char hard chunking, write duplication, and a dead knowledge graph.

Design principle adopted from upstream: **mechanical capture is the meal, agent-called memory tools are the garnish.** Upstream never trusts the AI to save — Stop/PreCompact/SessionEnd hooks mine transcripts mechanically; the AI only adds distilled judgment on top ("two-layer capture"). Paperclip2 currently has this inverted.

## 1.5.1 Run-teardown capture (replaces the §1.4 trigger) — highest priority

Move all automatic capture out of the `finish_task` closure (`native_engine.go` finish callback) into the run-teardown path, firing on **every terminal status** — `completed`, `failed`, `canceled`, timeout. Detached goroutine with its own timeout (upstream SessionEnd pattern: background, never delays teardown). Captures, in order of value:

1. **Auto-diary** from `result_description`/`result_explanation` + final status — synthesized even when the run died mid-flight ("run ended with status failed; last status: <current_status>"). Skip only if the agent already wrote a diary (`MempalaceProxy.DiaryWritten()`).
2. **Artifacts → drawers.** Each artifact written during the run is ingested (`mempalace_add_drawer`, `source_file = artifacts/<filename>`, room by content, closet = task). Pilot evidence: the 22-file frontend fix plan was invisible to recall — only the diary *mentioning* it was findable.
3. **Mechanical KG facts** — no LLM involved: `task-<id> --status--> <terminal status>`, `task-<id> --blocked_by--> <blocker>` when the result marks the task blocked, `task-<id> --produced--> artifact:<filename>`. This makes `memory_facts` return something for the first time (pilot: 7 calls, 0 results, KG contained 1 junk triple).

Idempotency: drawer IDs keyed on content-hash + source (upstream's mined-file sentinel recipe) so retried teardowns never duplicate.

## 1.5.2 Chunking fix — stop shredding agent memories

Pilot evidence: every stored doc capped at exactly `DEFAULT_CHUNK_SIZE = 800` chars; recall returned mid-word fragments ("rontend:** React 19…", "uld block npm install"). Measured `remember` payloads: 600–1,200 chars typical, ~2,300 max — i.e. almost every memory gets sliced.

- Set palace config `chunk_size` ≥ 2400 with sentence-boundary splitting and non-zero `chunk_overlap` (config plumbing already exists in `mempalace/config.py`; wire it into palace provisioning in `pkg/mempalace`).
- Agent-authored `remember`/diary payloads are already distilled units — they should land as **one drawer, unchunked** whenever under the cap.
- Prompt addition (CEO/CTO/Coder configs): "one fact per `remember` call" — improves recall precision and makes dedup meaningful.

## 1.5.3 Dedup on write

Pilot evidence: the "no shell tooling" platform limitation was stored as ~7 near-identical memories by CEO/CTO/Coder across runs 83–95; nobody ever called `memory_invalidate`.

- `remember` wrapper: run `mempalace_check_duplicate` (or a recall with similarity threshold) before `add_drawer`; on a strong match return "already known: <existing drawer excerpt>" instead of writing. Zero agent-prompt change needed.
- Engine-side ingestion (1.5.1) uses the same guard.
- Note the structural cause: parallel sibling tasks can't recall what siblings haven't finished writing. Mitigations: dedup-on-write (above) + wing-level wake-up refresh (the hourly cache in `pkg/mempalace.WakeUp` shortens to 5 min while any run in the wing is active, so late-starting runs see fresh facts).

## 1.5.4 KG extraction fix (refinement-store)

`storeRefinementMemory` currently promotes a markdown heading to a KG entity (pilot: `task-dec-59 --approach--> "## Task DEC-59: … — Complete"`). Replace `firstLine(content)` as the object with a stripped, prose-only summary (strip `#`/formatting, require ≥ N alpha chars, cap 140); skip the fact entirely rather than store junk. Publish the entity-naming convention (`task-<ref-key-lowercase>`) in the Memory prompt section so agents' `memory_facts` queries (pilot: `DEC-62`, `task-dec-65-1` — all misses) can actually hit.

## 1.5.5 Transcript mining at teardown + long-run checkpoints (bridge to Phase 2)

Upstream's `--mode convos` miner chunks transcripts **by Q+A exchange pair** with per-file idempotency — it does not dump raw logs (which would poison top-k recall with tool-schema noise).

- **Teardown mine:** normalize the run's session log into the JSONL shape `convo_miner` accepts and run `mempalace mine <dir> --mode convos --wing <project>` in the teardown goroutine. Gives verbatim recall of what actually happened in dead runs (91/93 in the pilot left zero trace).
- **Checkpoint mine (upstream Stop-hook analog):** every `SAVE_INTERVAL` (default 15) assistant turns in `executeSession`, background-mine the transcript-so-far; per-run `last_save` counter (upstream's `${SESSION_ID}_last_save` state-file pattern → a field on the run). Protects 20-min runs from losing everything on a crash. This is also the natural forerunner of Phase 2's PreCompact-equivalent: when compaction lands, the same mine runs synchronously before any turn is dropped.
- Optional one-time in-session nudge (upstream verbose mode): after a checkpoint mine, inject a single "save checkpoint: record durable learnings with remember" system note, guarded by a per-run flag. Ship dark, behind `memory.checkpoint_nudge`.

## 1.5.6 Protocol slimming

With 1.5.1 in place the mandatory pre-`finish_task` `write_diary` becomes redundant ceremony (and was the main source of duplicate blocker-memories). Prompt changes:

- `write_diary` → optional ("use it when you have judgment to add beyond the automatic record").
- `remember` repositioned: only for things no pipeline can know — hard-won learnings, "don't try X, it 404s".
- Remove "treat memory_facts as current truth" until 1.5.1/1.5.4 give the KG real content; reinstate afterwards.
- Document the `room` filter on `recall_memory` with one example (pilot: never used).

## 1.5.7 Acceptance criteria

1. A run killed with `kill -9` mid-session still yields: auto-diary, its artifacts as drawers, a `status` KG fact, and (with 1.5.5) exchange-pair drawers from the transcript-so-far.
2. Two agents storing the same fact produce one drawer; the second `remember` returns "already known".
3. No stored drawer ends mid-word; a 1,200-char `remember` is retrievable as a single intact result.
4. `memory_facts task-dec-<n>` returns the task's status/approach facts after any terminal run; no KG object contains markdown syntax.
5. Runs 91/93-style failures (agent never calls `finish_task`) show `engine:auto-diary` + `engine:teardown-ingest` rows in `memory_activities`.
6. Teardown capture adds zero latency to run completion (all async) and is idempotent under retry.

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

Sections 1–3 are produced by the shared `BuildTaskBriefing` builder (§1.2 scope note) with `querySeed` = last 2 user/assistant messages. Ordered sections, total ≤ `BridgeMaxTokens`:
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

1. **P1 wave 1 (foundations):** F1–F4, 1.1, 1.2, 1.5 — agents get memory tools. *(~1 wk)* ✅ shipped; validated in the DEC-59–65 pilot.
2. **P1 wave 2:** 1.3, 1.4, 1.6, 1.7, 1.8 — refinement hooks + full UI + backup. *(~1.5 wk)* ✅ shipped (1.4 trigger superseded by 1.5.1).
3. **P1.5 (post-pilot fixes — next up):** 1.5.1 teardown capture → 1.5.2 chunking → 1.5.3 dedup → 1.5.4 KG fix → 1.5.6 protocol slimming, then 1.5.5 transcript mining. 1.5.1–1.5.4 are small, independent, and each directly fixes a defect observed in production. *(~1 wk)*
4. **P2:** 2.1–2.6 (2.7 later). Compaction ships default-off, enabled per company in settings, flipped to default-on after the e2e suite + one week of dogfooding. 1.5.5's checkpoint mine becomes the PreCompact-equivalent guarantee (sweep-before-drop). *(~1 wk)*

Risks tracked from plan §5: Python runtime (F1 flag), embedding download (background init), write concurrency (F3 lock + daemon), version drift (pinned version + `repair` on restore).
