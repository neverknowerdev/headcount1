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
