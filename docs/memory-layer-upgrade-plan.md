# Memory Layer Upgrade Plan

Implementation plan for the top recommendations of
[`memory-layer-design-review.md`](./memory-layer-design-review.md), in three phases:

1. **Phase 1 — Bank consolidation**: one memory bank per company.
2. **Phase 2 — Observation-aware recall** (+ token budgets, bank config).
3. **Phase 3 — Mental models**: synthesized, auto-refreshing knowledge injected into
   briefings and prompts.

Each phase is independently shippable and e2e-testable; later phases assume earlier ones.

---

## Phase 1 — Bank consolidation

**Goal.** Replace the split `proj-<projectID>` / `runs-<companyID>` banks with a single
`company-<companyID>` bank so entity resolution, graph recall, rank fusion and (later)
observations and mental models see docs *and* run experience together.

### Target layout

| Concern | Before | After |
|---|---|---|
| Bank | `proj-<pid>` + `runs-<cid>` | `company-<cid>` only |
| Doc identity | document `doc:<relpath>` in project bank | document `doc:<pid>/<relpath>` in company bank (prefix keeps paths unique across projects) |
| Doc tags | `project:<pid>`, `source:docs` | unchanged |
| Run identity | document `run-<runID>` in runs bank | unchanged, now in company bank |
| Run tags | `agent:*`, `session:*`, `task:*`, `project:*` | unchanged |
| Recall | 2 requests, client-side concat | 1 request, native rank fusion |

Tag schema is untouched — it was already designed for a shared bank.

### Code changes

**`pkg/hindsight/service.go`**
- Replace `ProjectBankID`/`CompanyBankID` with a single `BankID(companyID int32) string`
  → `fmt.Sprintf("company-%d", companyID)`.
- `SyncProjectDocs`: takes the company (or companyID) as a parameter; retains into the
  company bank with `DocumentID: fmt.Sprintf("doc:%d/%s", project.ID, rel)`. The
  `hindsight_documents` table already stores `DocumentID` per row, so removal keeps
  working; existing rows are migrated (below).
- `Recall`: single `c.Recall` against the company bank. Keep the `agentRole` filter
  (`tags: [agent:<role>]`, `tags_match: any_strict`). Drop the projectID fan-out
  parameter from the signature (`projectID` stays only if we later want
  `tag_groups`-based narrowing; not needed now — doc memories are relevant
  company-wide and rank fusion will prioritize).
- `TaskBriefing`: unchanged behavior, one bank.

**`engine/native_engine.go`** — update the two `e.memory.Recall(...)` /
`TaskBriefing(...)` call sites for the new signatures. No behavioral change.

**`server/controllers/memory.go`**
- `ListMemoryBanks`: return one bank per company:
  `{bank_id: company-<id>, kind: "company", label: "Memory — <company>"}`. (The UI keeps
  the selector; with one bank per company it collapses to a single entry — leave the
  component generic.)
- `SyncProjectMemory` / `StartProjectMemoryInit` / `SyncAllProjectMemory`: pass company
  through to the new `SyncProjectDocs` signature.

**`pkg/hindsight/transfer.go`** — `ourBank` accepts the `company-` prefix; keep accepting
`proj-`/`runs-` on **import** so old backups restore (imported into their legacy bank ids;
see migration).

**Frontend `Memory.tsx`** — no structural change; optionally add a filter chip on the
memory list by tag (`project:<id>`, `agent:<role>`) since one bank now mixes sources.
(Nice-to-have; the list endpoint already passes `q` through.)

### Migration

Startup, one-shot, guarded by a marker (e.g. a `hindsight_meta` row or
`data/hindsight/.bank-v2` file):

1. If legacy banks exist (`ListBanks` shows `proj-*`/`runs-*` for our companies):
   export each via document-transfer, import into `company-<cid>` with
   `on_conflict=new-id`… **however** document-transfer import preserves document ids, and
   legacy doc ids (`doc:<relpath>`) don't carry the project prefix. Simpler and fully
   deterministic alternative (preferred):
   - **Docs**: wipe `hindsight_documents` rows for migrated projects and re-run
     `SyncProjectDocs` — docs are re-derivable from the repos at zero data-loss risk
     (LLM re-extraction cost only, one-time).
   - **Runs**: export `runs-<cid>` transfer archive → import into `company-<cid>`
     (`on_conflict=skip`); run document ids (`run-<id>`) are already unique.
   - Delete legacy banks after successful import.
2. If Hindsight is unreachable at startup, retry the migration next time the manager
   reports healthy (hook it after `Manager.Start` succeeds in `main.go`, before
   `SyncAllProjectMemory`).

### E2E / mock

- `mock-hindsight-server.ts`: no new endpoints needed (banks are auto-created); keep
  `__admin/dump` shape.
- `memory.spec.ts`: assert docs and run memories land in `company-<cid>`; assert the
  CMO recall test still surfaces CTO memories (now single-request); add a migration test:
  pre-seed mock with legacy `proj-*`/`runs-*` banks via retain, restart-free variant —
  call the migration through a new `POST /api/e2e/...`? Not worth an endpoint: instead
  seed legacy banks, wipe-db, restart is impossible in-suite → **test migration at the
  service level with a Go unit test** (`pkg/hindsight/migrate_test.go` against a stub
  HTTP server), and keep e2e focused on the new layout.
- `backup_restore.spec.ts`: assert `company-<cid>.zip` appears in the archive and
  round-trips; add one legacy-archive fixture case if cheap.

**Exit criteria.** All e2e green; a manual run shows one bank in the Memory UI with both
doc and run memories; entity graph shows doc- and run-derived nodes together.

---

## Phase 2 — Observation-aware recall, budgets, bank configuration

**Goal.** Surface Hindsight's consolidated observations, align token budgets with the
recommended tiers, and give the bank a mission/disposition so reflect (and Phase 3
mental models) reason with the right personality.

### 2a. Observation-aware recall

**`pkg/hindsight/client.go`** — extend `RecallRequest` with
`PreferObservations bool \`json:"prefer_observations,omitempty"\``.

**`pkg/hindsight/service.go`** — in `Recall` and `TaskBriefing`:
```go
req.Types = []string{"world", "experience", "observation"}
req.PreferObservations = true
```
`FormatResults` already prints the `type`; prefix observation results with `[insight]`
so agents see they're consolidated knowledge.

**Observation scopes.** On bank ensure (2c), set per-tag observation scopes so
consolidation happens at the levels we query: retain calls pass
`observation_scopes: ["per_tag"]`? — no: `observation_scopes` is a **retain item** field.
Set it on both retain paths:
- doc retain items: `ObservationScopes: [][]string{{"project:<pid>"}}`
- run retain items: scopes for `{agent:<role>}`, `{project:<pid>}` (and the shared scope)
Exact value shape is Hindsight's scope list (`combined`/`shared`/`per_tag`/custom sets) —
use `"per_tag"` string mode if accepted at item level, else explicit tag-set lists.
Verify against `hindsight_api` models during implementation (item field
`observation_scopes` exists on `MemoryItem`; add it to our Go struct).

### 2b. Token budgets

- `memory_recall` tool: `max_tokens` 2048 → **6144** default; add an optional
  `max_tokens` tool argument (bounded 512–16384).
- `TaskBriefing`: 2048 → **4096** (it costs every session's prompt; stay leaner).
- Make both overridable via `Settings` (`memory_recall_max_tokens`,
  `memory_briefing_max_tokens`) in `server/controllers/settings.go` +
  `engine/system_prompt.go`'s settings mirror; plumb through `Service`.

### 2c. Bank configuration (mission / disposition / directives)

API (verified against hindsight-api source):
- `PATCH /v1/default/banks/{bank_id}/config` body
  `{"updates": {"reflect_mission": "...", "disposition_skepticism": 4,
  "disposition_literalism": 3, "disposition_empathy": 1}}`
- Directives: `POST /v1/default/banks/{bank_id}/directives`.

**`pkg/hindsight/client.go`** — add `UpdateBankConfig(ctx, bankID, updates map[string]any)`
and `CreateDirective(ctx, bankID, name, content string)`.

**`pkg/hindsight/service.go`** — add `EnsureBank(ctx, company db.Company)`:
idempotently applies (marker in `hindsight_documents`-style meta table or an
`ensured` in-process set + config read-back):
- `reflect_mission`: "I am the collective long-term memory of {company}'s AI agent team.
  I track project documentation, implementation state, task outcomes and mistakes so
  agents don't repeat them."
- disposition: skepticism 4, literalism 3, empathy 1.
- directives: "Always state which task or run a claim comes from.";
  "Never present a blocked or failed attempt as a completed implementation."

Call `EnsureBank` lazily from the first retain/recall per company per process lifetime
(cheap guard), and from the Phase 1 migration.

### E2E / mock

- Mock: accept `prefer_observations`/`types` (store & echo); implement
  `PATCH /banks/{id}/config` and directives CRUD (store in bank state, expose in
  `__admin/dump`); optionally synthesize a fake `observation` memory after N retains of
  the same document tag so recall-type filtering is testable.
- `memory.spec.ts`: assert bank config lands in the mock after first use; assert recall
  requests carry the three types + `prefer_observations` (mock records last recall body
  → expose via `__admin/dump`).

**Exit criteria.** Recall bodies include observations; bank config visible via
`GET /api/memory/banks/{id}/stats`-adjacent proxy (add a `GET /config` proxy for the UI
if trivial); budgets configurable.

---

## Phase 3 — Mental models

**Goal.** Standing, auto-refreshing syntheses fetched as cheap lookups and injected where
agents need them: per-project state, per-agent playbook, global open blockers.

### API surface (verified)

- Create: `POST /v1/default/banks/{bank}/mental-models` body
  `{"id": "custom-id", "name", "source_query", "tags": [...], "max_tokens": 2048,
  "trigger": {"refresh_after_consolidation": true}}` → `{mental_model_id, operation_id}`
  (content generated async; poll operations or just read later — placeholder text until
  ready).
- Fetch: `GET .../mental-models/{id}` (fast lookup). Refresh: `POST .../{id}/refresh`.
- **Custom ids are allowed** (lowercase alnum + hyphens) — we exploit this for
  deterministic retrieval, no ID storage table needed.

### Model catalog (deterministic ids)

| id | name | source tags | source_query | max_tokens | injected into |
|---|---|---|---|---|---|
| `project-state-<pid>` | Project state: {name} | `["project:<pid>"]` | "What is the current implementation state of this project? Cover: what is implemented and working, key technical decisions, current blockers or failures, and what was attempted but not completed." | 2048 | pre-task briefing for tasks with this project |
| `agent-playbook-<role>` | Playbook: {ROLE} | `["agent:<role>"]` | "What working approaches, recurring mistakes, and lessons has this agent accumulated across its runs? Focus on actionable do's and don'ts." | 1024 | that role's system prompt (all sessions) |
| `open-blockers` | Open blockers | `[]` | "What unresolved blockers, failed attempts and recurring errors currently exist across tasks? Ignore anything that a later run resolved." | 1024 | CEO sessions' briefing |

Lifecycle:
- `project-state-<pid>`: created by `StartProjectMemoryInit` (after first doc sync) and
  ensured on task execution for the project.
- `agent-playbook-<role>`: ensured on first session of that role per process lifetime.
- `open-blockers`: ensured with `EnsureBank`.
- All created with `refresh_after_consolidation: true` → no manual refresh path needed;
  keep a "Refresh" button per model in the UI calling the refresh endpoint.

### Code changes

**`pkg/hindsight/client.go`** — `CreateMentalModel(ctx, bank, req)`,
`GetMentalModelRaw(ctx, bank, id)`, `ListMentalModelsRaw`, `RefreshMentalModel`,
`DeleteMentalModel`.

**`pkg/hindsight/mentalmodels.go`** (new) —
- `EnsureProjectStateModel(ctx, companyID, project)`, `EnsureAgentPlaybookModel(...)`,
  `EnsureOpenBlockersModel(...)` — create-if-missing (GET by deterministic id; 404 →
  create). In-process `sync.Map` guard to avoid hammering.
- `FetchModelContent(ctx, companyID, modelID) (string, bool)` — GET, parse `content`,
  return ok=false for placeholder/missing/error (callers skip the section).

**`engine/native_engine.go`** — assemble the briefing as the skill's canonical pattern
(*mental model fetch + recall, never reflect*):
```
## Memory briefing
### Project state (synthesized)        ← project-state-<pid>, if task has a project
### Your playbook (synthesized)        ← agent-playbook-<role>
### Open blockers (synthesized)        ← open-blockers, CEO sessions only
### Related memories (recall)          ← existing TaskBriefing recall
```
Each section only rendered when content is real. Budget guard: cap total injected
briefing at ~6k tokens (truncate recall section first).

**`server/controllers/memory.go` + routes** — proxy endpoints:
`GET /api/memory/banks/{bank}/mental-models`, `GET/DELETE .../mental-models/{id}`,
`POST .../mental-models/{id}/refresh`. (Creation stays app-driven; no create endpoint in
the UI initially.)

**Frontend `Memory.tsx`** — new "Insights" tab: list mental models (name, updated time),
view content (markdown), refresh button, delete. Small scope — read-mostly.

**Export/import (review finding #7 — must ship in this phase).**
`pkg/hindsight/transfer.go`:
- Export: alongside `<bank>.zip`, call `GET /v1/default/banks/{bank}/export` (bank
  template manifest: config + mental models + directives) → `<bank>.template.json`.
- Import: `POST /v1/default/banks/{bank}/import` with the manifest **before** the
  document-transfer import (bank + config exist first), then documents; mental models
  refresh themselves after consolidation.

### E2E / mock

- Mock: mental-model CRUD + refresh (content = canned synthesis: concatenate top
  memories matching tags, prefix "SYNTHESIS:"), `GET/POST /banks/{id}/export|import`
  (JSON manifest round-trip), refresh-after-consolidation simulation: on retain, mark
  tagged models stale and regenerate.
- `memory.spec.ts` additions mirroring the CTO/CMO story:
  1. After CTO's blocked run retains, `project-state-<pid>` regenerates → its content
     contains the blocker; assert the CMO session's **system prompt** (visible in the
     mock LLM provider's recorded request) contains the "Project state" section with it.
  2. After CTO's successful rerun, model contains the success; CMO rerun's prompt shows
     the updated state.
  3. UI proxy smoke: list models, fetch content, refresh, delete.
- `backup_restore.spec.ts`: assert `<bank>.template.json` in archive; after restore +
  mock reset, models and bank config are back (via `__admin/dump`).

**Exit criteria.** Briefings show synthesized sections sourced from mental models;
models auto-update in the mock flow; backup round-trips banks losslessly *including*
config, directives and mental models.

---

## Cross-cutting

- **Ordering**: Phase 1 → 2 → 3 strictly (2 assumes one bank; 3 assumes config + scopes).
- **Failure posture** (unchanged principle): memory must never block or fail a run —
  every new call is best-effort with logging and bounded timeouts.
- **Cost**: Phase 2 raises recall budgets (retrieval-only, no LLM). Phase 3 adds
  background reflect runs per model refresh on the **cheap utility model**; with
  refresh-after-consolidation they trigger only when retains actually consolidate.
- **Docs**: update `pkg/hindsight/service.go` header comment and
  `memory-layer-design-review.md` status column as phases land.
- **Estimated effort**: Phase 1 ~1 day (incl. migration test), Phase 2 ~0.5–1 day,
  Phase 3 ~2 days (engine + UI + mock + e2e).
