# MemPalace Phase 1.5 — Implementation Spec (self-contained)

**Status:** ready to implement. **Scope:** exactly the Phase 1.5 work agreed on 2026-07-04.
**Background docs (optional reading):** `docs/mempalace-tech-spec.md` (Phase 1.5 section), `docs/mempalace-integration-plan.md` (§7 post-pilot addendum). This file is self-sufficient — an agent should be able to implement from this file alone.

---

## 1. Context: what exists today (do not re-implement)

The MemPalace memory layer (Phase 1) is shipped and running:

- **`pkg/mempalace/`** — palace lifecycle: `Available()`, `ServerForCompany()`, `Ready()`, `CallServerTool()` (shared cached MCP client), `WakeUp()` (CLI `wake-up`, cached 1h, capped 4800 chars), `MineProject()`, `Resolve()` addressing (company→palace, project→wing, task→closet, run→source_file). CLI binary: `setup.MempalaceCLIPath()`; MCP binary: `setup.MempalaceMCPPath()`. Palace dir per company: `PalacePath(company)`.
- **`engine/aicli/tools/mempalace.go`** — `MempalaceProxy`, agent-facing tools: `recall_memory`, `recall_run`, `remember`, `memory_facts`, `write_diary`, `memory_invalidate`, `recall_company`. Already implemented there:
  - `remember` enforces `rememberMaxChars = 500` and one-fact-per-call (description + hard reject) — **done, keep as is**;
  - `remember` runs `mempalace_check_duplicate` at threshold **0.95** before storing;
  - `MempalaceProxy.DiaryWritten()` flag.
- **`engine/memory_integration.go`** — engine hooks: `resolveMemorySession`, `memoryPromptSection` (Palace Protocol block in system prompt), `planModeRecall` (plan-mode "prior work" injection), `storeRefinementMemory` (+ KG approach fact), `autoDiary`, `recordEngineMemoryActivity`.
- **`engine/native_engine.go`** — `executeSession` orchestrates a run. The `finish_task` closure (search for `NewFinishTask`) currently calls `storeRefinementMemory` (plan mode) and `autoDiary` (when `!mpProxy.DiaryWritten()`) in a goroutine. Terminal paths that do NOT go through `finish_task`: `failRun()` (~line 1638), the `context.Canceled` branch in `executeSession` (~line 919), agent-error → `status = "failed"` path (~line 925).
- **`engine/aicli/agent.go`** — `pruneHistory` (~line 455) silently truncates stale tool results and caps history at 60k chars; called on every request build (~line 208). No capture happens before it cuts.
- **`db.MemoryActivity`** table + `/api/.../memory` API + Memory UI page — activity feed works; engine-initiated rows use `AgentName: ""` and tool names prefixed `engine:` (see `recordEngineMemoryActivity`).
- **Artifacts** — `write_artifact` tool stores flat files in the task-tree artifact dir + a `db.Artifact` row (`ListArtifactsByTaskTree`).
- **MemPalace server tools available via `CallServerTool`:** `mempalace_search`, `mempalace_add_drawer`, `mempalace_update_drawer`, `mempalace_check_duplicate`, `mempalace_diary_write`, `mempalace_kg_add`, `mempalace_kg_invalidate`, `mempalace_kg_query`, `mempalace_list_drawers`, `mempalace_get_drawer`. CLI verbs: `init`, `mine` (`--mode convos|projects`), `sweep`, `wake-up`, `repair`.

### Why this phase exists (pilot findings, runs 80–95, tasks DEC-59–65)

1. Failed/canceled runs (91, 92, 93) captured **nothing** — `autoDiary` lives inside the `finish_task` closure, which those runs never reach.
2. Artifacts are invisible to semantic recall (only diary text mentioning them was findable).
3. The knowledge graph is effectively empty: `memory_facts` returned 0 in all 7 calls; the only triple was junk (`task-dec-59 --approach--> "## Task DEC-59: … — Complete"` — a markdown heading stored by `storeRefinementMemory`).
4. Palace chunking is 800 chars, hard cut → recall returned mid-word fragments ("rontend:** React 19…").
5. The same fact ("no agent has shell tools") was stored ~7× by different agents; the 0.95 dup threshold missed differently-phrased duplicates.
6. Mandatory `write_diary` before `finish_task` produced ceremony + duplication.

**Design principle (from upstream MemPalace's own Claude Code/Codex hooks):** mechanical capture by the engine is the guarantee; agent-called tools add distilled judgment only. Two-layer capture.

---

## 2. Work items

Implement in this order. Each item is independently shippable; keep commits per item.

### W1. Run-teardown capture (highest value)

**Problem:** all automatic capture is tied to the `finish_task` tool. Runs that fail, get canceled, or hang never call it.

**Change:** create a single teardown hook, e.g. `func (e *NativeEngine) memoryTeardown(mem *memorySession, task db.Task, run db.Run, terminalStatus string)` in `engine/memory_integration.go`, and call it from **every** terminal transition of a run:

- the `finish_task` closure in `executeSession` (replacing the current goroutine that calls `storeRefinementMemory`/`autoDiary` — move that logic into the hook),
- the `context.Canceled` branch in `executeSession`,
- the agent-error `status = "failed"` path in `executeSession`,
- `failRun()` (early failures — memory session may be nil there; skip when nil).

Semantics:

- Detached goroutine, own `context.WithTimeout(context.Background(), 2*time.Minute)`. Never blocks or delays run completion. All failures logged, never propagated.
- Guard against double-fire (e.g. `finish_task` followed by the post-loop path): add a `sync.Once` or an atomic flag per run session.
- What it does, in order:
  1. **Auto-diary** (existing `autoDiary`, extended): fire when `mpProxy == nil || !mpProxy.DiaryWritten()`. For non-completed statuses synthesize from what exists: `"Run ended with status %q."` + `result_description` if set, else the run's `current_status` field, else the error message. Record activity as `engine:auto-diary`.
  2. **Artifact ingestion:** for each `db.Artifact` created by *this run* (filter `ListArtifactsByTaskTree` by `RunID`), store a drawer: `wing` = project wing, `room` = `general` (or `decisions` if the artifact description contains "decision" — keep it simple), `content` = `"[<closet>] [artifact <filename>] <description>\n\n" + file content`, capped at 8000 chars, `source_file` = `artifacts/<filename>`, `added_by` = agent name. Run `mempalace_check_duplicate` first (idempotency under retries). Record as `engine:artifact-ingest` with `result_n` = number ingested.
  3. **Mechanical KG facts** (no LLM):
     - `task-<refkey-lower> --status--> <terminalStatus>` — invalidate the previous `status` fact for this entity first (same pattern as `storeRefinementMemory` does for `approach`).
     - if the finish status string or result contains "blocked" (case-insensitive): `task-<refkey-lower> --blocked_by--> <first line of result, prose-stripped per W4, cap 140>`.
     - one `task-<refkey-lower> --produced--> artifact:<filename>` per ingested artifact.
     Record as `engine:kg-facts`.
  4. **Refinement store** (existing `storeRefinementMemory`) when `mode == "plan"` — unchanged behavior, just relocated, plus the W4 fix.

**Acceptance:** kill a run mid-session (cancel via API or `kill -9` the LLM call path in a test) → `memory_activities` shows `engine:auto-diary` (+ `engine:artifact-ingest` if artifacts existed); `memory_facts` for the task returns the `status` fact. Teardown firing twice stores nothing twice.

### W2. Chunking config — stop shredding memories

**Problem:** palace default `chunk_size` is 800 chars, hard character cut (verified: every stored doc in chroma is exactly ≤800, fragments cut mid-word). Typical agent memories are 600–1,200 chars.

**Change:** in `pkg/mempalace` palace provisioning (where a palace is created/seeded — `ServerForCompany`/`EnsureBuiltinMCPServers` path), set palace config: `chunk_size: 2400`, `chunk_overlap: 200`, `min_chunk_size: 100`. MemPalace reads these from the palace's config (see `mempalace/config.py` in the installed package — `DEFAULT_CHUNK_SIZE = 800`; config values are coerced/validated by `MempalaceConfig`). Mechanism: whichever the package supports — palace-level `config.json` in the palace dir or env vars passed to the MCP server process (`MEMPALACE_*`); inspect the installed package under the shared venv (`setup.PythonInterpreter()` venv, `site-packages/mempalace/config.py`) to confirm the exact key names and mechanism before wiring.

Existing palaces: chunk config affects only *new* writes; do not re-chunk old data (acceptable — pilot data is throwaway).

**Acceptance:** after provisioning, store a 1,200-char drawer via `mempalace_add_drawer` → `mempalace_search` returns it as one intact result, not fragments.

### W3. Dedup that actually catches near-duplicates

**Problem:** `remember`'s `check_duplicate` at 0.95 missed all 7 pilot duplicates (different phrasing of the same fact).

**Change** in `engine/aicli/tools/mempalace.go` `remember.execute`:

1. Lower `check_duplicate` threshold to **0.83** (tune later; make it a package const `rememberDupThreshold`).
2. If `check_duplicate` is inconclusive/unsupported, fall back to `mempalace_search` (wing-scoped, limit 1) and treat top-1 `similarity >= rememberDupThreshold` as duplicate.
3. On duplicate, return the existing text so the agent learns what's already known: `"Already in memory (similar: \"<first 200 chars of existing>\") — not stored again."`
4. Apply the same guard inside W1's artifact ingestion.

**Acceptance:** storing "No agent has shell tools — builds can't run" then "PLATFORM LIMITATION: agents lack shell execution so make build fails" → second call returns "Already in memory", one drawer total.

### W4. KG extraction fix + entity naming

**Problem:** `storeRefinementMemory` uses `firstLine(content, 200)` as the KG object → stored a markdown heading as an entity/object. Agents also guess entity names (`DEC-62`, `task-dec-65-1`) and always miss.

**Change:**

1. In `engine/memory_integration.go`, add `func kgSummary(s string, max int) string`: strip markdown (`#`, `*`, `` ` ``, `[]()`, leading list markers), collapse whitespace, require ≥ 20 alphabetic chars — otherwise return `""`; cap at `max` (140). Use it for the `approach` object in `storeRefinementMemory` and all W1 fact objects. **If it returns "", skip the fact entirely** — junk facts are worse than no facts.
2. Entity naming convention: `task-<refkey-lowercase>` (e.g. `task-dec-62`). Single helper `taskEntity(task db.Task) string` used by every KG write (W1, `storeRefinementMemory`) and set as `TaskEntity` in `resolveMemorySession` (verify current value matches; today it uses `addr.Closet` — keep them identical).
3. Tell agents the convention: in `memoryPromptSection`, extend the Memory block: `"Knowledge-graph entities are named task-<ref> (e.g. task-dec-62); the current task is <entity>."`
4. In the same block, **remove** the sentence "Treat memory_facts (knowledge graph) as current truth … facts win" until the KG has real content (W1 ships it); replace with: `"memory_facts lists structured facts (status, approach, blockers) for a task entity."`

**Acceptance:** after a plan run + a terminal run, `memory_facts` on `task-<ref>` returns ≥ 2 facts, none containing `#` or other markdown; `memoryPromptSection` output contains the entity name.

### W5. Transcript mining: teardown + checkpoints + pre-prune

**Problem:** nothing verbatim survives a dead run; `pruneHistory` already drops tool results today with no capture; raw session logs (1–7 MB JSON, ~1% signal) must never go into drawers directly.

**Change:**

1. **Transcript writer** (`engine/aicli/transcript.go`): append one JSON line per history message: `{"role","content","ts","session_id"}` — the shape `mempalace mine --mode convos` consumes (verify against `site-packages/mempalace/convo_miner.py`: it ingests chat-export JSONL, chunks by Q+A exchange pair, idempotent per file). Path: under the run's log dir (`<data>/<company>/logs/<task>/run-<id>/transcript-<session>.jsonl`). Truncate tool-result content to 4 KB per message before writing (miner input hygiene). Wire into the agent loop where history messages are appended (`engine/aicli/agent.go`, `runMessageHistory`); flush per message.
2. **Mine helper** in `pkg/mempalace`: `MineTranscript(company, wing, dir string)` → `exec.CommandContext(… CLI …, "--palace", PalacePath, "mine", dir, "--mode", "convos", "--wing", wing, "--agent", agentName)`, under the company lock (follow `MineProject`'s pattern). Idempotent by design (miner sentinels) — safe to call repeatedly on the same dir.
3. **Teardown mine:** call `MineTranscript` from W1's teardown hook (after diary/artifacts).
4. **Checkpoint mine:** in the engine layer (not aicli), count assistant turns per session; every **15** turns kick a background `MineTranscript`. Store the counter on the session struct — no DB field needed. (Upstream Stop-hook analog, `SAVE_INTERVAL=15`.)
5. **Pre-prune mine (synchronous):** `pruneHistory` decisions live inside `engine/aicli` which must stay mempalace-free. Add an optional callback on the aicli agent config: `OnPrune func(dropped []Message)` (or a pre-prune hook `func() error`). The engine wires it to a **synchronous** `MineTranscript` — upstream's rule: the no-loss guarantee is the sync mine, not AI compliance. If the mine errors: log to `memory_activities` (`engine:preprune-mine`, `result_n=0`) and let the prune proceed — memory must never stall a run. Since mining is idempotent, only the delta since the last checkpoint costs anything.
6. Record activities: `engine:transcript-mine` (teardown), `engine:checkpoint-mine`, `engine:preprune-mine`.

**Guardrail:** never pass raw `main.log`/`session-N.log` to `mine` — only the purpose-built transcript JSONL.

**Acceptance:** (a) cancel a run after ~10 turns → `recall_run` finds verbatim content from turn 3; (b) a fact present only in a tool result that `pruneHistory` truncated is retrievable afterwards; (c) mining failure doesn't block the prune or the run; (d) re-running teardown mine adds zero new drawers.

### W6. Protocol slimming (prompts + tool surface)

1. **Remove `write_diary` from the agent tool surface** — drop it from `mpCatalog` in `engine/aicli/tools/mempalace.go` (or filter it from registration). Keep `mempalace_diary_write` engine-side for the auto-diary. Keep `DiaryWritten()` (still meaningful if a role is re-enabled later via `AllowedTools`); if a run has no agent diary, W1's auto-diary covers it.
2. **Update `memoryPromptSection`** (`engine/memory_integration.go`): remove "Before finish_task, call write_diary."; reposition remember: `"Use remember only for durable learnings no log can convey (e.g. 'X fails because Y — do Z instead'). One fact per call, max 500 chars. The engine records run history, artifacts and status automatically."` Keep the recall-first sentence. Apply W4's memory_facts wording here too.
3. **Agent config prompts** (`engine/agentconfig/prompts/*.md`): remove any write_diary mentions; keep/add the one-fact-per-remember line if present.
4. Mention the `room` filter once in the recall guidance: `"recall_memory accepts room='decisions' to search decisions only."`

**Acceptance:** tool listing in a new run's LLM request contains no `write_diary`; system prompt contains the new remember guidance and no diary mandate; after a clean run without agent memory calls, exactly one diary entry (auto) exists.

### W7. Wake-up freshness for parallel runs (small)

**Problem:** wake-up context is cached 1 h (`wakeUpCache` in `pkg/mempalace/manager.go`), so parallel sibling tasks never see facts written minutes earlier.

**Change:** track active runs per company (the engine knows); while any run is active for a company, use a 5-minute TTL for that palace's wake-up cache entries (simplest: expose `mempalace.SetWakeUpTTL(palacePath, d)` or check a callback). Do not go lower than 5 min — wake-up shells out to the CLI.

**Acceptance:** run A writes a memory; run B starting 6+ min later in the same wing gets a wake-up block that can include it (verify the CLI call happened, not the cached copy).

---

## 3. Non-goals (explicitly out of scope for this phase)

- Context compaction / memory-bridge messages (Phase 2 in `mempalace-tech-spec.md`). W5's transcript+mine machinery is its foundation, but do not modify `pruneHistory`'s truncation logic beyond adding the pre-prune hook.
- Re-chunking or migrating existing palace data.
- The optional in-session "save checkpoint" nudge (`memory.checkpoint_nudge`) — skip unless trivial.
- UI changes beyond what already renders (`memory_activities` feed picks up the new `engine:*` tool names automatically).

## 4. Cross-cutting requirements

- Every new engine-side memory operation records a `memory_activities` row (use `recordEngineMemoryActivity`; `AgentName` stays "" for engine-initiated).
- Memory must never fail or slow a run: all teardown/mine work in goroutines with timeouts (except the deliberate synchronous pre-prune mine, which still must swallow errors).
- All writes go through the existing addressing (`memorySession.scope` / `mempalace.Resolve`) — never freestyle wing/room/closet names.
- `go build ./... && go test ./engine/... ./pkg/...` green; add unit tests for `kgSummary`, the dedup path (mock proxy), teardown double-fire guard, and transcript-line format.
- Update the Phase 1.5 section of `docs/mempalace-tech-spec.md` only if implementation deviates; otherwise leave docs untouched.
