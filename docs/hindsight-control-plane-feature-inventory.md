# Hindsight Control Plane — Feature Inventory and Re-Implementation Estimate

## Why this document exists

We considered adding a link from our Memory section to Hindsight's own web UI.
That turned out to be unsafe and impractical (see "Why we are not linking to it"
below), so the alternative is to re-implement the parts we want inside our own
Memory page, where our team authorization already applies. This document
inventories what the Hindsight Control Plane actually does, compares it to what
we already ship, and estimates the cost of closing the gap under our
multi-user and encryption constraints.

**Method.** Findings are read from the shipped artifact, not from marketing
docs: `@vectorize-io/hindsight-control-plane@0.8.5` unpacked from npm
(Next.js 16 standalone build, ~71 MB). The feature list is derived from its
route manifest (`app-path-routes-manifest.json`, 58 API routes and 4 pages) and
its bundled English i18n catalogue (33 sections, ~1,500 strings), which names
every view, column, dialog, and toast. Version note: the control plane is 0.8.x
while we pin `HINDSIGHT_PIN="0.6.1"`, so a few endpoints listed here may not
exist on our pinned API — each one needs checking before it is built.

## Why we are not linking to it

- **It is not part of what we install.** `hindsight-api==0.6.1` is a 3.1 kB
  meta-package over `hindsight-api-slim[all]==0.6.1`. That wheel contains no
  HTML/JS/CSS and no `StaticFiles` mount — the Python server exposes the REST
  API and `/mcp` only. The control plane is a separate Node application, so
  linking to it means adding a Node runtime and a second supervised process.
- **Its auth model cannot express our tenancy.** It reads
  `HINDSIGHT_CP_DATAPLANE_API_URL`, `HINDSIGHT_CP_DATAPLANE_API_KEY` (used
  server-side — it does *not* leak the key to the browser), and
  `HINDSIGHT_CP_ACCESS_KEY`. The last is a **single shared secret**: one
  password, one privilege level, and when it is unset the middleware lets
  `/api/*` through with **no authentication at all**. Its CLI also defaults to
  `hostname = 0.0.0.0`.
- **It sits below our authorization layer.** Hindsight has no concept of our
  teams: all banks live in the one `default` tenant and `GET /v1/default/banks`
  returns every one of them. Our isolation is entirely app-side
  (`LoadMemoryBank` → `authorizeCompany` → canonical bank id, in
  `server/controllers/authz_mw.go` and `pkg/hindsight/service.go`). Anyone who
  reaches the control plane sees and edits **every team's** memory.

---

## What the Control Plane provides

It has only four pages — `/login`, `/` , `/dashboard`, and
`/banks/[bankId]` — with essentially the whole product living in the tabbed
bank page. Sizes below are the count of i18n strings in each section, a decent
proxy for surface area.

### Navigation and shell
Bank switcher with search/create/copy-name, dark-mode toggle, locale routing
(`/[locale]/...`, en + de shipped), and an access-key login page.

### 1. Overview / stats (`bankStats`, 64 strings)
Memory-store counters (memories, documents, links); composition split across
**world / experience / observations**; link-type breakdown (temporal, semantic,
entity); consolidation status with failed-memory drill-in; mental-model
freshness (up-to-date / stale); an **LLM health test** with typed failure states
(not configured, auth failed, unreachable, timeout); operations summary
(completed/processing/pending/failed/cancelled); and a memories-ingested
time-series chart pivotable by ingested / mentioned / occurred date.

### 2. Memories browser (`dataView`, 77 strings)
Three view modes over the same data:
- **Table** — columns for observation, memory, sources, tags, entities,
  created / mentioned / occurred; text and tag filters; paging via "load more".
- **Timeline** — zoomable, with year/month/week/day granularity, paging
  controls, a configurable recency basis, and a separate bucket for
  memories without dates.
- **Constellation** — a force/scatter visualization with zoom, pan, hover,
  click-select, fullscreen, colour-by selector, a link-type legend, a live HUD
  (memories / visible / labels / links / zoom), and **SVG export**.

Plus consolidation state ("in sync" vs pending count) and a detail panel/modal
(`memoryDetailModal` + `memoryDetailPanel`, 82 strings) with per-memory history.

### 3. Documents (`documentsView` 92, `addDocument` 75, `documentChunkModal` 18)
Paginated document table (id, created, updated, tags, metadata, size, memory
units) with search; a detail drawer showing original text, retain params
(context, event date, metadata), tags, and memory-unit count; inline **edit** of
document text; delete with cascade warning; chunk inspection; and
**reprocess** / **transfer** operations. The add-document dialog supports
pasted text or **file upload** (drag-and-drop, multi-file, per-file metadata),
two extraction parsers (standard / enhanced), context, event date, custom
document id, tags, observation scopes, and async mode.

### 4. Entities (`entitiesView`, 27 strings)
Entity list (name, mentions, first seen, last seen) plus a **co-occurrence
graph** with heat and size legends; entity detail with mentions, first seen, id,
and related observations; and entity **regeneration**.

### 5. Mental models (`mentalModels` 113, `mentalModelDetailModal` 77)
Dashboard and table views; create / edit / delete; source query and max-tokens
configuration; **refresh triggers** — manual, auto, or **cron-scheduled**
(with a `cronPreview` human-readable next-run) — showing last-refreshed and
next-refresh; manual refresh; history; and clear.

### 6. Bank profile (`bankProfile`, 64 strings)
Editable **disposition traits** (skepticism, literalism, empathy — the sliders
that shape how Hindsight forms opinions), bank **mission** text, and full CRUD
over **directives** (name, rule, tags).

### 7. Bank configuration (`bankConfig`, 149 strings — the largest section)
Deep per-bank settings with an inherited-vs-override model and per-field "reset
to inherited": retain settings (chunk size, structured chunk size, mission,
extraction mode, custom extraction prompt), entity settings (entity-label
editor, free-form entities), observation settings (enable, mission, scopes),
reflect settings (mission, disposition), and a **tool enable/disable matrix**.

### 8. Recall analyzer / search debug (`searchDebug`, 54 strings)
Runs a recall and **visualizes the retrieval pipeline**: parallel retrieval
methods, RRF fusion with per-stage scores, cross-encoder rerank, and final
results — with duration, nodes-visited, token counts, tag-match modes
(any/all/strict/exact), fact-type and date filters, and raw-JSON view.

### 9. Reflect / think (`thinkView`, 77 strings)
Runs `reflect` with a token budget (low/mid/high), tag filters, exclusions, and
include-source / include-tools switches; then shows the answer, **new opinions**
with confidence, observations created, a step-by-step **execution trace** of
tool calls with inputs/outputs, the supporting facts, and raw JSON — plus
inline "save as directive" feedback.

### 10. Operations (`bankOperations`, 58 strings)
Async job monitor across retain, consolidation, mental-model refresh,
file-convert-retain, webhook delivery, and graph maintenance: status filters,
progress with heartbeat and estimate, sub-batch counts, raw payload inspection,
and **cancel / retry / delete** actions.

### 11. Webhooks (`webhooksView`, 77 strings)
Full CRUD over outbound webhooks — URL, method, timeout, event-type selection,
**shared secret** (with show/hide/clear), custom headers, query params, and
enable/disable — plus a delivery log with status, HTTP code, attempt count,
error body, and response body.

### 12. Audit logs (`auditLogsView`, 52 strings)
Request-volume chart plus a filterable log of ~17 action types (retain, recall,
reflect, bank CRUD, consolidation, mental-model and directive changes,
file-convert-retain, webhook delivery, memory defense) by transport
(HTTP / MCP / system) and date range, with a detail dialog exposing the full
request, response, and metadata.

### 13. LLM requests (`llmRequestsView`, 65 strings)
Call-volume and token-volume charts (input / output / **cached** / total,
cumulative or per-period), filterable by status and operation, with a detail
dialog showing provider, model, duration, input, output, error, trace and span
ids, and a link to the trace.

### 14. Bank actions (`bank`, 97 strings)
Run consolidation, recover a failed consolidation, clear observations, reset
configuration, export a bank **template**, import from template, dry-run
extraction against arbitrary text, and delete the bank behind a type-the-name
confirmation.

---

## What we already have

`frontend/src/pages/Memory.tsx` (1,585 lines) with four tabs — **Graph**
(memories and entities modes), **Memories** (list with search and type filter,
project filter), **Query** (recall + ask), **Insights** (mental models) —
served by ~21 authorized routes registered in `server/handlers.go:307` and
proxied in `server/controllers/memory.go` (519 lines): graph, entities-graph,
memory unit list/get/update/delete, recall, ask, stats, config (read-only),
directives (read-only), tags, and mental-model CRUD + refresh + history.

So we already cover, at least partially: the graph, the memory list, entity
viewing, recall, and mental models. The control plane exposes **58** API routes
against our **21**.

---

## Gap analysis and estimate

**Assumptions.** One experienced full-stack engineer (Go + React) working in
this codebase, reusing the existing proxy pattern
(`memoryTenantClientOr` + `LoadMemoryBank` + `memoryBankFromCtx`) and the
existing Memory page shell. Estimates are **engineer-days** and include the
backend proxy route, the React view, tests, and the multi-user checks below —
not design iteration or product review. Ranges reflect genuine uncertainty,
mostly about which endpoints exist on our pinned 0.6.1.

| # | Area | Status today | Est. (days) |
|---|---|---|---|
| 1 | Stats/overview parity (composition, link types, LLM health, ingest time-series) | Partial (`/stats`) | 3–5 |
| 2 | Memories table parity (columns, tag filter, paging) | Partial | 3–4 |
| 2b | Timeline view | None | 4–6 |
| 2c | Constellation view + SVG export | Partial (graph exists) | 5–8 |
| 3 | Documents: list, detail, edit, delete, chunks, reprocess, transfer | None | 8–12 |
| 3b | Add-document dialog incl. multi-file upload and parsers | None | 5–8 |
| 4 | Entities: list, co-occurrence graph, detail, regenerate | Partial | 3–5 |
| 5 | Mental models: cron scheduling, dashboard view, clear | Mostly done | 3–5 |
| 6 | Bank profile: disposition, mission, directive CRUD | Read-only | 4–6 |
| 7 | Bank configuration (149 strings, inherit/override model) | Read-only | 10–15 |
| 8 | Recall analyzer with retrieval-pipeline visualization | None | 6–9 |
| 9 | Reflect view with execution trace and opinions | Partial (`/ask`) | 5–8 |
| 10 | Operations monitor with cancel/retry/delete | None | 5–7 |
| 11 | Webhooks CRUD + delivery log | None | 6–9 |
| 12 | Audit logs + volume chart | None | 5–7 |
| 13 | LLM request/token analytics | None | 5–7 |
| 14 | Bank actions (consolidate, recover, clear, reset, template, dry-run, delete) | None | 4–6 |
| — | **Feature subtotal** | | **80–127** |

### Cross-cutting work our system requires that the control plane does not do

The control plane is a single-tenant admin tool; everything below is work it
never had to do, and it is the part most likely to be underestimated.

| Area | Why it is needed | Est. (days) |
|---|---|---|
| Team authorization on ~37 new proxy routes | Every route must go through `LoadMemoryBank` and derive the bank id from the authorized company (`memoryBankFromCtx`), never from the URL param — the exact bug fixed in the canonicalization pass | 4–6 |
| Role gating | Destructive actions (delete bank, reset config, clear observations, restore) need a policy. Roles are flat today (`TeamRoleOwner`/`TeamRoleMember`, `db/models.go:129`) with only invites owner-only; a `RequireGlobalAdminAPI` gate exists for operator surfaces. Deciding and applying the split is real work | 3–5 |
| **Encryption / secrets** | Webhook shared secrets and any provider credentials surfaced in these views must be sealed with `pkg/secrets` per-user DEKs (`EncryptForUser`, `enc:u1:` prefix), never returned to the client, and must handle `ErrLocked` when the vault is locked — including a sane UI state for "locked" | 5–8 |
| **Encryption boundary review** | Memory content itself lives in Hindsight's Postgres **unencrypted** — outside our zero-knowledge model. Audit logs and LLM-request logs would newly surface prompt and response bodies in the UI. This needs an explicit decision and documentation before those views ship | 2–4 |
| Export/import interaction | Bank template export/import and document transfer must not cross team boundaries; they interact with the existing `/data` export/import and the operator-only `/backup` path | 3–5 |
| Version pinning against 0.6.1 | Confirm which of these endpoints exist on our pinned API; either raise `HINDSIGHT_PIN` (and re-verify the tenancy findings) or drop the unavailable views | 2–4 |
| e2e coverage | Isolation specs for each new route in the mock backend, in the style of `e2e/tests/memory_isolation.spec.ts` | 5–8 |
| **Cross-cutting subtotal** | | **24–40** |

### Bottom line

**Full parity: roughly 104–167 engineer-days (~5–8 months for one engineer).**
That is not a recommendation — full parity is almost certainly the wrong goal.
The control plane is an operator console for a product team that owns the
backend; much of it (webhooks, audit logs, LLM analytics, the 149-string config
editor, operations) is infrastructure tooling rather than something our users
need.

**A defensible first slice — roughly 20–30 days:**

1. Stats/overview parity (#1) — 3–5
2. Memories table + timeline (#2, #2b) — 7–10
3. Bank profile: disposition, mission, directives CRUD (#6) — 4–6
4. Bank actions, minus destructive ones (#14, partial) — 2–3
5. The authorization and encryption cross-cutting work for just those routes — 4–6

That delivers the parts users would actually open, keeps every request inside
`LoadMemoryBank`, and defers the operator-console features until there is
demand. The remaining items stay documented here so the decision to skip them is
explicit rather than accidental.

## Open questions

- Do we raise `HINDSIGHT_PIN` from 0.6.1? Several views above depend on
  endpoints that may only exist in 0.8.x, and raising the pin means re-verifying
  the tenancy findings in `memory-layer-design-review.md`.
- Should memory content be encrypted at rest on the Hindsight side? Today it is
  not, which is a visible gap against the zero-knowledge model the rest of the
  product follows.
- Which destructive actions, if any, should a `TeamRoleMember` be able to take?
