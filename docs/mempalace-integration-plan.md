# MemPalace Integration — Investigation Report & Implementation Plan

**Date:** 2026-07-04
**Subject:** Integrating [MemPalace](https://github.com/MemPalace/mempalace) as the memory layer for the agent orchestrator.

---

## TL;DR

**Yes — all four requested use cases are feasible**, and paperclip2 is unusually well-prepared for this integration:

- We already have a **mature MCP client layer** (stdio + HTTP transports, per-agent tool filters, a generic `call_mcp_tool` / `discover_mcp_tool` dispatcher).
- We already have a **precedent for an auto-provisioned per-project MCP server**: codegraph (`server/controllers/projects.go`, `engine/aicli/tools/codegraph.go`). MemPalace can follow the exact same pattern.
- The codebase contains explicit hooks left for a memory feature: `AgentConfig.MemoryTags` ("used by the memory bank (future feature)", `engine/agentconfig/config.go:52`), the naive `engine/memory.go` (`memory.md` per task), and a **dead stub** `integration/mempalace.go`.

**One important correction:** the existing stub installs mempalace via **npm** from `github.com/milla-jovovich/mempalace`. The real MemPalace is a **Python** package — installed via `uv tool install mempalace` / `pipx` / Docker, MCP server binary `mempalace-mcp`. The npm stub must be replaced.

**Recommended integration mode:** run MemPalace as an external MCP server (stdio for agents, its CLI/daemon for lifecycle ops), one palace per company, wing per project/agent. The Go backend talks to it through our existing `engine/mcp` client — no Python bindings needed.

---

## 1. What MemPalace is (research summary)

- **Local-first AI memory** system: *verbatim* storage of content + semantic search. Explicitly does **not** summarize/paraphrase — it stores originals and retrieves relevant pieces (96.6% R@5 on LongMemEval, zero API calls for search).
- **Language/packaging:** Python 3.9+ library + CLI. Install: `uv tool install mempalace`, pipx, pip, or Docker (CPU/GPU images). MIT license.
- **Storage:**
  - Vector store: **ChromaDB** (default), pluggable: `sqlite_exact`, `qdrant` (REST), `pgvector` (PostgreSQL). Configured via env vars (`MEMPALACE_PGVECTOR_DSN`, `MEMPALACE_QDRANT_URL`, …).
  - **Temporal knowledge graph** (entities/relations with validity windows — add, query, invalidate, timeline) in local SQLite.
  - Local embedding models: `embedding-gemma-300m` (multilingual, recommended) or `all-MiniLM-L6-v2` (~30 MB, English).
- **Memory model:** hierarchical "palace" — **wings** (people/projects), **rooms** (topics), **drawers** (verbatim content, auto-chunked). **Tunnels** = explicit cross-wing links; **hallways** = within-wing links. Searches can be scoped to wing/room instead of a flat corpus.
- **Agent support:** each specialist agent can get its own wing + **diary** (`mempalace_diary_write` — what happened / what was learned / what matters), discoverable at runtime via agent-listing tools.
- **MCP server:** `mempalace-mcp` — **~34 tools** over **stdio or HTTP** (`--transport`). Categorized read (19) / write (11) / maintenance (3). Key tools:
  - Read: `mempalace_status`, `mempalace_list_wings`, `mempalace_list_rooms`, `mempalace_get_taxonomy`, `mempalace_search`, `mempalace_check_duplicate`, `mempalace_traverse_graph`, `mempalace_find_tunnels`, `mempalace_graph_stats`, `mempalace_list_tunnels`, `mempalace_list_hallways`, `mempalace_follow_tunnels`
  - Write: `mempalace_add_drawer`, `mempalace_update_drawer`, `mempalace_delete_drawer`, `mempalace_delete_by_source`, `mempalace_create_tunnel`/`delete_tunnel`, `mempalace_delete_hallway`, `mempalace_kg_add`, `mempalace_kg_invalidate`, `mempalace_diary_write`, `mempalace_mine`, `mempalace_sync`
- **CLI:** `init` (detect rooms from folder structure, optional LLM-assisted refinement — supports ollama / openai-compat / anthropic providers), `mine` (index project files, conversations `--mode convos`, office docs), `search`, `wake-up` (emits a ~600–900-token L0/L1 identity+context block for the model), `compress` (AAAK dialect, ~30x reduction into a separate collection, keeps searchability), `sweep` (tandem miner over `.jsonl` transcripts — **one verbatim drawer per user/assistant message, idempotent, resume-safe**), `sync`, `split`, `daemon` (opt-in long-lived background job queue: start/stop/status/jobs/wait), `repair`, `migrate`.
- **"Palace Protocol":** on wake-up the server teaches the agent to call `mempalace_status` first, verify facts via search before answering, and write a diary at session end.
- **Programmatic API caveat:** the package exports only `__version__` publicly; internal modules are not a stable API. **Integrate through the MCP server + CLI, not Python imports.** This also means our Go backend never needs Python bindings.

## 2. Where paperclip2 stands (codebase findings)

| Area | State | Key files |
|---|---|---|
| MCP client | Mature: stdio + HTTP transports, lazy sessions, tool cache, per-agent filters | `engine/mcp/`, `engine/aicli/tools/discover_mcp.go`, `native_engine.go:694-821` |
| Per-project MCP server precedent | codegraph: auto-created, init lifecycle, startup sweep, proxy registration | `server/controllers/projects.go:122,276`, `engine/aicli/tools/codegraph.go`, `main.go:153` |
| MCP server registry | DB rows + builtin seeding + npm dep auto-install | `db/models.go:203`, `db/queries_mcp.go:391`, `pkg/setup/npm.go` |
| Conversation storage | Rebuilt per run from Task/Comments/Runs/Artifacts; in-loop history in memory; run logs as JSON/files | `native_engine.go:930` (`buildInitialMessages`), `engine/aicli/agent.go:194` |
| Context compaction | Truncation only (`pruneHistory` drops stale tool results, caps at 60k chars); **no summarization layer** | `engine/aicli/agent.go:455`, constants `:47-59` |
| Task refinement | First-class: `refinement` status + `plan` run mode; output in `RefinedDescription`/`AcceptanceCriteria`/`TestCases` | `native_engine.go:172-192`, `db/models.go:112-114` |
| Memory hooks (unused) | `MemoryTags` on agent config; `memory.md` per task; dead npm installer stub | `engine/agentconfig/config.go:52`, `engine/memory.go`, `integration/mempalace.go` |
| Frontend | React 19 + Vite + Tailwind; sidebar nav array; page-per-section pattern; **no graph-viz lib installed** | `frontend/src/components/Sidebar.tsx:6-19`, `App.tsx`, `pages/MCPServers.tsx` |
| DB | GORM AutoMigrate (SQLite default, 1 conn; Postgres via `DATABASE_URL`) | `main.go:41-97` |

## 3. Proposed architecture

```
┌──────────────────────────── paperclip2 (Go) ────────────────────────────┐
│                                                                         │
│  Agents ──► aicli.Registry ──► call_mcp_tool ─┐                         │
│                                               │  (existing engine/mcp   │
│  Engine hooks (refinement, compaction) ───────┤   stdio client)         │
│                                               │                         │
│  /api/memory (UI backend) ────────────────────┤                         │
│                                               ▼                         │
│                                    mempalace-mcp (stdio)                │
│                                    --palace <home>/memory/<company>     │
│                                                                         │
│  Lifecycle (init/mine/sweep/compress) ──► mempalace CLI / daemon        │
└─────────────────────────────────────────────────────────────────────────┘
```

Decisions:

1. **One palace per company** at `<PaperclipHome>/memory/<company-shortname>/`. Wings map to projects and to agents (agent diaries). This gives tenant isolation for free and matches MemPalace's own scoping model.
2. **Transport: stdio** via the existing `engine/mcp` stdio client, seeded as a builtin `db.MCPServer` (`Command: "mempalace-mcp"`, args `--palace <dir>`) — same as codegraph. HTTP transport stays available as an option (e.g., Docker sidecar deployment) since our MCP client already supports both.
3. **Lifecycle operations** (init, mine, sweep, compress, sync) go through the **CLI / daemon queue**, invoked from Go with `exec.Command` — the same way codegraph init works today. The daemon serializes writes, which protects ChromaDB from concurrent-writer issues.
4. **Installation:** replace the npm stub with a `uv tool install mempalace` (fallback `pipx`) step in the `pkg/setup` startup goroutine; record install status. Docker image is the fallback for hosts without Python.
5. **Backend choice:** ChromaDB default for SQLite deployments; when `DATABASE_URL` (Postgres) is set, optionally use `pgvector` backend (`MEMPALACE_PGVECTOR_DSN`) so all state lives in one database.

## 3.1 Taxonomy mapping: palace concepts ↔ orchestrator concepts

MemPalace's structural model is **Wings → Rooms → Closets → Drawers** (per mempalaceofficial.com): wings are *entities* ("entity-first, always"), rooms are *discrete units of time* (days/sessions — "walk the corridor and the palace unfolds chronologically"), closets *group related drawers by topic/thread within a room*, drawers are *verbatim chunks*.

Our hierarchy has more levels than the palace's four. **Do not encode every level structurally** — recall is scoped semantic search, not path navigation, and over-nesting fragments the corpus. Top levels map to structure; run/session levels go into drawer **metadata**; relationships go into the **knowledge graph**.

| Orchestrator concept | Palace concept | Notes |
|---|---|---|
| Company | **Palace** (one per company) | Hard tenant isolation: own directory + MCP server instance. |
| Project | **Wing** `project:<shortname>` | Entity-first. |
| Agent | **Wing** `agent:<name>` | MemPalace's agent-diary pattern; diaries + per-agent learnings only. |
| Cross-project knowledge | **Wing** `company` | People, conventions, org-wide decisions. |
| Sprint | **Room** `sprint-<label>` | The natural time unit; chronological corridor. |
| Durable knowledge | **Rooms** `architecture`, `decisions`, `requirements` | Evergreen topical rooms per project wing. |
| Task / sub-task | **Closet** `task-<id>` | "Every drawer on that subject together." Sub-tasks get own closets; parent link via KG/hallway. Lives in the sprint room it ran in. |
| Message / artifact / plan chunk | **Drawer** | One drawer per user/assistant message (`sweep` semantics), plus refined plans, artifacts, decisions. |
| Run / run session / sub-session | **Drawer metadata** | `source_file = runs/<task>/<run>/<session>.jsonl`, `added_by = <agent>`. Searchable via `source_file` filter; cleanable via `delete_by_source`. |
| Task↔subtask, decisions, dependencies | **Knowledge graph** | `mempalace_kg_add` with validity windows ("uses Postgres since sprint-3"). |
| Related work across projects | **Tunnels** | Explicit cross-wing bridges (e.g. auth in app ↔ auth in api). |

Search-scope defaults per flow: refinement → `wing=project` (all rooms); compaction recall → current task closet, widening to wing; agent recall → project wing + own agent wing + `company` wing (cross-agent diary access is a per-role permission).

**Implementation note:** closet assignment happens at filing time (and `compress` buckets by closet), so writes must not freestyle the taxonomy. Centralize a "memory addressing" helper in Go that maps `(company, project, sprint, task, run, session, agent)` → `(palace, wing, room, closet, source_file, added_by)` and use it on every write path (agent tool wrapper, sweep, refinement store, UI corrections).

## 3.2 Invalidation: handling pivots ("we went another way")

Principle: **history never becomes wrong, but conclusions do** — and MemPalace separates the two. Rule of thumb: *delete is for wrong, supersede is for outdated.*

**Tier 1 — verbatim record (message/run drawers): never invalidated.** "We tried approach A" stays true after pivoting to B; it answers later questions like "why didn't A work?". The risk is only that recall presents old plans as current — solved at the recall layer, not by deletion.

**Tier 2 — current truth (KG + decision records): explicitly superseded on pivot.**
1. `mempalace_kg_invalidate` the overturned facts (temporal KG closes their validity window; timeline preserved).
2. `mempalace_kg_add` the new facts, with a `supersedes` relation + reason.
3. Write a superseding ADR-style drawer in the `decisions` room ("Decision 12 supersedes 9: WebSockets → SSE because…"), hallway-linked to the old one; mark the old plan drawer superseded via `mempalace_update_drawer`.

**Epistemic ordering at recall (Palace Protocol addition):** agents (and our recall-digest injection) check valid KG facts first; drawers are historical record. On conflict, KG wins. Digest snippets carry their date and superseded markers.

**Who invalidates:**
- **Engine (automatic, primary):** when a task re-enters `refinement` or a new plan replaces an existing one — supersede old plan drawer, write superseding decision, swap KG facts.
- **Agents:** planner-level roles (CEO/CTO) get `kg_invalidate` + prompt instruction "when you change direction, invalidate what you're overturning"; specialists don't invalidate.
- **Humans (Memory UI):** per-node actions *edit* / *delete* / **mark superseded** (safe default), plus a KG timeline view.

**Poisonous-memory escalation ladder** (when an abandoned approach pollutes recall): 1) supersede (usually enough once recall prefers KG-validated results); 2) retag closet to `task-<id>-abandoned` so default scoped recall skips it; 3) `mempalace_delete_by_source` on the run's `source_file` (`dry_run` first) — run/session-level `source_file` encoding is the undo granularity.

## 3.3 Recall query model: what queries are possible

Recall is **not a query language** — it is three complementary subsystems (verified in `searcher.py` / `knowledge_graph.py` / `layers.py`):

1. **Semantic top-k search** (`mempalace_search`): hybrid ranking — vector similarity (0.6) + BM25 keyword (0.4), over-fetch + re-rank, ±1 neighbor-chunk expansion on strong hits. Handles conceptual queries ("why did we drop WebSockets?") and exact strings/identifiers (BM25). Always top-k (default 5) with optional `max_distance` gate.
2. **Scoped search filters:** exactly `wing`, `room`, `source_file` — i.e. per-project, per-sprint/topic, per-run/session (thanks to our `source_file` encoding). **No** boolean/negation/aggregation, no date-range, no `added_by` filter. It ranks, it doesn't SELECT.
3. **Knowledge-graph structured queries** (complete result sets, not top-k): `query_entity(name, as_of, direction)`, `query_relationship(predicate, as_of)`, `timeline(entity)` — temporal triples with validity windows. "What's true about X (as of date D)?", "all relations of type P", "history of X incl. invalidated facts".
4. **Structural browsing:** `get_taxonomy`, `list_wings/rooms`, `traverse_graph` (BFS), `find_tunnels`/`follow_tunnels`. Plus `check_duplicate` before writes.
5. **Layered recall stack** (`layers.py`): L0 identity → L1 critical facts (`wake-up`, ~600-900 tokens) → L2 room recall → L3 deep search.

**Exhaustive queries ("fetch ALL tool errors in run DEC-10"):** not native to drawer search (top-k ≠ all). Handle by design:
- *Approximation:* `search(query="tool error failed", source_file="runs/DEC-10/…", n_results=30)` — good for "remind me", not an audit.
- *Ingest-time KG facts (recommended):* the sweep wrapper classifies messages and emits facts (`run:DEC-10 --had_tool_error--> tool:exec_command`, provenance → drawer id) for a curated set of event types (errors, decisions, files touched, subtask spawns). Then "all X in Y" = complete KG query linking back to verbatim drawers.
- *Own DB:* exhaustive per-run operational queries stay on `Run.LogEntries` (`/api/runs`); MemPalace answers the semantic follow-up ("have we seen this error before in any project?").

## 3.4 Recall scoping: does the agent name wings/rooms?

**Rule: the agent must never invent wing/room names; the engine provides the current scope, the agent may widen/narrow within permission.**

Two recall paths, two scoping rules:

- **Engine-injected recall** (refinement digest, compaction digest, wake-up): fully automatic. The engine's addressing helper resolves `(company, project, sprint, task)` → `(wing, room, source_file)` and injects results into the prompt — the agent never calls a tool or names anything.
- **Agent-initiated recall** (mid-session `mempalace_search` via `call_mcp_tool`): `wing` is effectively mandatory in practice (search within one company's palace but across projects mixes unrelated drawers into top-k ranking — no query language exists to exclude them after the fact). `room`/`source_file` are optional, added when the question is sprint- or run-scoped.

| Ask | Scope to pass |
|---|---|
| "recall anything about the current task" | `wing=<project>` only |
| "what did we decide this sprint" | `wing=<project>`, `room=<sprint>` |
| "what happened in that earlier run" | `wing=<project>`, `source_file=<run path>` |
| explicit cross-project ask ("have we hit this error anywhere before?") | `wing` omitted or set to `company` wing — **permission-gated**, not every role |
| agent doesn't specify | fallback default: current project wing, never a fully unscoped palace search |

**Implementation:** inject the current project wing name + task closet as plain facts in the system prompt (never let the agent slugify its own names — that's the addressing helper's job). Recommended: don't expose raw `mempalace_search` to most roles — wrap it in a `recall_project_memory(query, room?, source_file?)` tool that server-side injects `wing` and can't be overridden; reserve raw/unscoped `call_mcp_tool` access to `mempalace_search` for CTO/CEO-tier roles that legitimately need cross-project recall.

## 3.5 Export / import & backup integration

MemPalace has **no native export/import command**, but none is needed: a palace is a local directory where **SQLite stores are authoritative and the vector index is derived** — `mempalace repair --mode from-sqlite` rebuilds vectors from SQLite; `migrate` handles ChromaDB version drift. This matches our restore philosophy exactly (`pkg/backup/restore.go` rebuilds the DB from the filesystem).

**Durable truths** (must survive): drawer content + metadata, KG triples w/ validity windows, tunnels/hallways, embedder identity. **Rebuildable** (never protect, always regenerate): embeddings / HNSW index.

**Format A — physical (regular backups):**
1. Quiesce writes (hold the per-company write lock in our addressing helper; stop mempalace daemon).
2. Add `<PaperclipHome>/memory/<company>/` to the existing tar (`addDirectoryToTar` in `pkg/backup/backup.go`). Optionally exclude HNSW binary files (bulky, 100% rebuildable) to keep the 7-backup rotation small.
3. Restore: extract → `mempalace repair --mode from-sqlite` → `mempalace sync --dry-run` to validate. Self-heals index corruption and version drift (`mempalace migrate` as fallback).

**Format B — logical JSONL (portable: migration, sharing, backend switch):**
Self-implemented dump: one line per drawer `{wing, room, closet, content, source_file, added_by, created_at}` + KG triples + tunnels + manifest (embedder identity, versions). Import = replay via `add_drawer`/`kg_add`, idempotent (content-hash dedupe / `check_duplicate`). Slower (re-embeds everything) but version- and backend-independent — also the ChromaDB→pgvector migration path.

**Flow integration:**
- Daily backup/restore: extend `CreateBackup`/`RestoreBackup` as above.
- Per-company export: palace-per-company makes it one self-contained directory — tar + optional JSONL, no disentangling.
- **Addressing rule (lock in now):** wing/closet/source_file must key on stable names (company shortname, project name, task display ID), never DB auto-increment IDs — `rebuildDBFromFS` reassigns IDs on restore and would orphan every reference otherwise.
- **Embedder identity gotcha:** vectors are meaningless under a different embedding model. Manifest records embedder; on mismatch at import, route to the logical (re-embed) path, not the physical one.
- pgvector deployments: vectors live in Postgres, not the palace dir — logical export is the only complete file-level story (or Postgres-native backup covers both).

## 4. Use-case mapping

### 4.1 Memory MCP tools for agents (recall) — ✅ easiest, near-zero engine change

- Seed a builtin `mempalace` MCP server in `EnsureBuiltinMCPServers` (`db/queries_mcp.go:391`); agents immediately get `mempalace_search`, `mempalace_add_drawer`, etc. through the existing `call_mcp_tool` dispatcher.
- Use the existing per-agent MCP tool filters (`db.AgentMCPToolFilter`, `AgentDetails.tsx` UI) to decide which agents may write vs only read memory. E.g., specialists get read+diary; CEO/CTO get full write.
- Attribution: pass agent name as `added_by` on `mempalace_add_drawer` and `--agent` on mining — this powers the per-agent UI view later.
- Add the **Palace Protocol** to system prompts: inject `mempalace wake-up --wing <project>` output (~600–900 tokens) into the system prompt or seed message, instructing agents to search memory before asking humans and to write a diary entry before `finish_task`.
- Repurpose `AgentConfig.MemoryTags` as default wing/room scoping per role.

### 4.2 `mempalace init` for task refinement — ✅ straightforward

- **At project creation** (codegraph pattern, `projects.go:122`): run `mempalace init --dir <workspace> --yes --no-llm` (or LLM-assisted via the company's configured provider — MemPalace supports `openai-compat`, which matches our `LLMProvider` rows and cheap `UtilityModel`), then `mempalace mine --dir <workspace> --wing <project>` in the background with an `InitStatus` lifecycle like codegraph's.
- **At refinement (plan mode)**, in `processTask`/`buildInitialMessages`:
  - Before planning: run `mempalace_search` with the task title/description scoped to the project wing, inject top-k results ("relevant prior decisions & work") into the seed message.
  - After refinement completes: store `RefinedDescription` + `AcceptanceCriteria` as a drawer (room: e.g. `tasks/planning`), and key decisions as KG facts (`mempalace_kg_add`) so later tasks can ask "why did we decide X".

### 4.3 Context compaction via MemPalace — ✅ feasible, most engine work

MemPalace's philosophy matches the request exactly: it does **not** summarize — it stores verbatim messages and retrieves the important parts on demand.

- **Persist run transcripts:** stream each session's messages to a `.jsonl` transcript per run (we already keep `Run.LogEntries`; add a message-level jsonl), then run `mempalace sweep <transcript>` at session end (or incrementally) — one drawer per user/assistant message, idempotent.
- **Extend `pruneHistory` (`engine/aicli/agent.go:455`)** into a real compaction stage:
  1. Keep the last N turns verbatim (today's `freshAssistantTurns` concept).
  2. For older turns: instead of silently truncating, sweep them into the palace, then replace them with a compact digest = top-k `mempalace_search` results relevant to the current task + a marker message: *"Older conversation moved to memory. Use `mempalace_search` (wing=<project>, source_file=<run-transcript>) to recall any earlier part verbatim."*
  3. The agent can then self-serve recall of any dropped message via the MCP tools — exactly the requested "agent can call recall on message history".
- **Cross-run continuity for free:** `buildInitialMessages` currently replays all past run results/comments; with memory in place it can inject only recent ones plus recalled highlights, shrinking seed size for long-lived tasks.
- Optional: nightly `mempalace compress` (AAAK, ~30x) on old wings to bound disk while keeping searchability.

### 4.4 "Memory" section in the UI — ✅ feasible; only real gap is a graph-viz library

Backend — new `/api/memory` chi router (`server/handlers.go`) that proxies MCP read tools through `engine/mcp` and shells to the CLI for maintenance:

| Endpoint | Backed by |
|---|---|
| `GET /api/memory/status`, `/taxonomy` | `mempalace_status`, `mempalace_get_taxonomy` |
| `GET /api/memory/wings`, `/rooms`, `/drawers` | `mempalace_list_wings/rooms` + search |
| `GET /api/memory/search?q=…&wing=…` | `mempalace_search` |
| `GET /api/memory/graph` | `mempalace_graph_stats`, `traverse_graph`, `list_tunnels`, `list_hallways` |
| `PUT/DELETE /api/memory/drawers/:id`, `POST /tunnels` | `mempalace_update/delete_drawer`, `create_tunnel` (corrections) |
| `GET /api/memory/activity` | new `db.MemoryActivity` table — log every mempalace tool call (agent, tool, wing/room, task/run id, ts) at the dispatcher level |
| `GET /api/memory/agents` | agent-diary tools + `added_by` aggregation |
| `POST /api/memory/maintenance/{mine,sync,compress}` | CLI/daemon jobs |

Frontend — nav item in `Sidebar.tsx`, route in `App.tsx`, page `pages/Memory.tsx` with tabs:
1. **Explorer** — wings → rooms → drawers tree (taxonomy) with drawer content viewer + inline edit/delete (corrections).
2. **Graph** — nodes (wings/rooms) + edges (tunnels/hallways). Requires a new dependency; **recommend `@xyflow/react` (React Flow)** — React-first, MIT, handles small/medium graphs well; `cytoscape.js` if graphs get large.
3. **Search** — semantic search box with wing/room filters, similarity scores.
4. **Activity & Stats** — from `MemoryActivity` + `mempalace_status`/`graph_stats`: reads/writes over time, top wings, drawer counts, per-agent usage.
5. **Agents** — per-agent memory: diary entries, drawers by `added_by`, tool-access toggles (reuse the `AgentDetails` MCP-filter component).

### 4.5 "Maybe something else?" — additional opportunities

- **Agent diaries as institutional memory:** call `mempalace_diary_write` automatically in the `finish_task` path — every run leaves "what happened / learned / matters". Great input for retro/board views.
- **Duplicate guard:** call `mempalace_check_duplicate` before storing refined plans/artifacts to keep the palace clean.
- **Cross-project knowledge ("tunnels"):** CTO-level agents can create tunnels linking related solutions across projects (`mempalace_find_tunnels` for discovery).
- **Backup integration:** include `<PaperclipHome>/memory/` in the existing `pkg/backup` daily backup.
- **Onboarding new agents:** wake-up context per role means a freshly-added specialist starts with the essential story of the company/project instead of a cold prompt.
- **`memory.md` retirement:** replace `engine/memory.go`'s static file with a wake-up snapshot written into the workspace.

## 5. Risks & watch-outs

1. **Python runtime dependency.** The Go binary must manage an external Python tool. Mitigation: `uv tool install` (self-contained venv) during `setup.Run`, health-check on startup, Docker image (`--transport http`) as fallback. Feature-flag memory so the app degrades gracefully when mempalace is absent.
2. **Embedding model download** (30 MB–~1 GB) on first init — do it in the background lifecycle (codegraph-style `InitStatus`), never on the request path.
3. **Write concurrency.** ChromaDB is single-writer-ish; multiple concurrent agent runs writing memory should funnel writes through the mempalace **daemon** queue (it exists for exactly this) or per-company serialization on our side.
4. **No stable Python API** — pin the mempalace version in setup; interact only via MCP + CLI (both are the documented surface).
5. **The existing npm stub is wrong** (`integration/mempalace.go` points at an npm repo that isn't this project) — delete/replace it.
6. **Token budget:** wake-up (~600–900 tokens) + recalled snippets must be capped; make counts configurable per role.
7. **Tenant isolation:** enforce palace path per company in the server args; never share one palace across companies.

## 6. Implementation plan (phased)

Each phase ships independently and is useful on its own.

**Phase 0 — Runtime & lifecycle (foundation), ~2-3 days**
- Replace `integration/mempalace.go` with a `pkg/setup` installer (`uv tool install mempalace`, version-pinned, health check `mempalace --version`).
- Palace-per-company directory under `PaperclipHome()`; seed builtin `mempalace` MCPServer (stdio, `--palace <dir>`) in `EnsureBuiltinMCPServers`.
- `mempalace init` + background `mine` on project creation, cloning the codegraph `InitStatus` lifecycle; startup sweep for unfinished inits.
- Feature flag + graceful degradation when the binary is missing.

**Phase 1 — Agent memory tools, ~2-3 days**
- Default `AllowedTools` entries (`mempalace_*` read for all roles; write/diary per role) + per-agent filters.
- Palace Protocol + `wake-up` block in system prompt (`engine/system_prompt.go`), cached per project.
- Auto `mempalace_diary_write` on `finish_task`; wire `MemoryTags` to default wing/room scope.
- `MemoryActivity` logging at the MCP dispatcher.

**Phase 2 — Task refinement integration, ~1-2 days**
- Recall injection in plan mode (`buildInitialMessages`): top-k scoped search on the task text.
- Persist refinement outputs as drawers + KG facts after plan-mode runs.

**Phase 3 — Context compaction, ~3-5 days (most engine risk)**
- Per-run message `.jsonl` transcript + `sweep` at session end.
- Rework `pruneHistory` → compaction: keep last N turns, swap older turns for recall digest + self-recall instructions.
- Guardrails: token caps, e2e test that a fact from a dropped turn is recoverable via recall.

**Phase 4 — Memory UI, ~4-6 days**
- `/api/memory` router (proxy read tools, drawer/tunnel mutations, activity, maintenance jobs).
- `pages/Memory.tsx` + sidebar entry: Explorer, Graph (add `@xyflow/react`), Search, Activity & Stats, Agents tabs.

**Phase 5 — Ops polish, ~1-2 days**
- Nightly `compress`/`sync` scheduler (alongside the 24h MCP tool-cache job); include palace in `pkg/backup`; optional `pgvector` backend when running on Postgres.

**Total: roughly 2.5–3.5 weeks of focused work**, with agents gaining working memory (Phases 0–1) after the first week.
