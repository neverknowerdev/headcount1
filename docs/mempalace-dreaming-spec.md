# Dreaming — Reflection Layer on MemPalace

**Status:** proposed (not yet implemented)
**Depends on:** `docs/mempalace-integration-plan.md` (taxonomy, KG model, addressing), `docs/mempalace-tech-spec.md` (F1–F4 foundations, Phase 1 agent tools), `docs/mempalace-phase-1.5-implementation.md` (teardown capture, checkpoint mining — Dreaming reads their output)
**Motivation:** see conversation summary below — MemPalace gives us verbatim recall + a temporal KG, but no synthesis step (Hindsight's "Mental Models" have no equivalent). Dreaming is a scheduled engine job that periodically re-reads accumulated memory per wing and writes back a small number of synthesized, evidence-linked insights — without adopting Hindsight's per-write LLM cost or losing MemPalace's verbatim fidelity.

---

## 1. What Dreaming is and isn't

**Is:** an engine-owned, scheduled batch job (default: daily) that reads recent drawers + valid KG facts for a wing, asks a cheap LLM to propose a *small, evidence-cited* set of durable patterns, and writes them back into the same palace as a distinct, clearly-provenanced class of memory (`kind=insight`).

**Isn't:**
- Not a replacement for MemPalace's verbatim storage or KG — it's a read-then-write pass *on top of* them, using the existing MCP tools (`mempalace_search`, `mempalace_kg_add`, `mempalace_add_drawer`).
- Not real-time. Agents never trigger it, never wait on it, never call an LLM for it mid-run. It only runs off-hours, out of the request/run path.
- Not agent-facing write access — no agent role gets a `dream` tool. It's an internal engine job, like the nightly `compress`/`sync` scheduler already planned in the tech spec's Phase 5.
- Not authoritative by default. Insights are marked as synthesized-and-unconfirmed until a human (or a planner-tier agent) promotes them; they never silently override KG facts written by teardown capture or agents.

## 2. Why "insights," not summaries

MemPalace's own design principle — *verbatim record never wrong, conclusions can be superseded* (integration plan §3.2) — extends cleanly to Dreaming:

- Tier 1 (verbatim drawers, KG operational facts): untouched by Dreaming. Ground truth.
- Tier 2 (KG current-truth facts, decision records): written by engine hooks + planner agents today.
- **Tier 3 (new) — dreamed insights**: patterns *inferred* across Tier 1/2 material, not stated by anyone directly. Recurring bug in this codebase, a reviewer's actual preference vs. stated one, "task estimates from this agent run ~2x over." These need their own tier because they're epistemically weaker than Tier 1/2 — they must carry evidence, confidence, and a review state, and must be easy to prune if wrong.

## 3. Data model additions

### 3.1 KG relation types

New predicate namespace, distinguishable from operational KG facts (`uses`, `supersedes`, `blocks`, …) added elsewhere in the system:

| Predicate | Meaning | Example triple |
|---|---|---|
| `pattern` | Recurring behavioral/technical pattern | `(project:acme, pattern, "CI flakes on the payments test suite ~1/3 of runs")` |
| `preference` | Inferred (not stated) preference of a person/role | `(person:jane, preference, "prefers SSE over WebSockets for status updates")` |
| `risk` | Recurring failure mode / risk signal | `(agent:coder-3, risk, "underestimates tasks touching the billing module")` |

Each dreamed triple carries extra fields beyond the base KG schema (validity window is reused as-is):

```
{
  subject, predicate, object,       // standard KG fields
  valid_from, valid_to,             // standard validity window
  confidence: float,                // 0.0–1.0, from the dreaming LLM call
  evidence_drawer_ids: [string],    // which drawers/facts this was inferred from
  review_state: "unconfirmed" | "confirmed" | "rejected",
  dream_run_id: string              // which DreamRun produced it (for rollback/audit)
}
```

MemPalace's KG schema doesn't natively have `confidence`/`evidence`/`review_state` — these are encoded as a JSON blob in the KG fact's existing free-text metadata field (same trick already used for `source_file`/`added_by` on drawers), not a MemPalace schema change.

### 3.2 Insight drawers

Alongside the KG triple, write one companion drawer per insight into a per-wing `insights` room:

- `content`: the insight statement in natural language + a one-line "why" (the reasoning the LLM gave).
- `source_file`: `dreaming/<dream-run-id>/<insight-index>` — keeps it queryable/deletable via `mempalace_delete_by_source`, consistent with the run/session `source_file` convention.
- `added_by`: `"dreaming"` — a synthetic agent identity, so it's visually and programmatically distinguishable from human/agent-authored drawers in the Memory UI and in `MemoryActivity`.

### 3.3 New table: `DreamRun`

Tracks incremental progress and provides an idempotency watermark (mirrors the `InitStatus` lifecycle pattern already used for codegraph/mempalace provisioning):

```go
type DreamRun struct {
    ID              int32     `gorm:"primaryKey"`
    CompanyID       int32     `gorm:"index"`
    Wing            string    // "project:<name>" | "agent:<name>" | "company"
    StartedAt       time.Time
    FinishedAt      *time.Time
    Status          string    // "running" | "completed" | "error" | "skipped-no-activity"
    WatermarkBefore time.Time // drawers/facts considered: created_at < WatermarkBefore
    WatermarkAfter  time.Time // new watermark on success; unchanged on error/skip
    InsightsAdded   int
    InsightsSkipped int       // deduped against existing insights
    InsightsSuperseded int    // contradicted an existing insight
    AuditConfirmed  int       // existing facts revalidated (freshness bump, no write)
    AuditContradicted int     // existing Tier 2/3 facts invalidated by the audit pass (§4a)
    Error           string
    PromptTokens    int
    CompletionTokens int
}
```

One row per (company, wing, night). `WatermarkAfter` becomes next run's `WatermarkBefore` floor — bounds the LLM input to only what's new since the last successful dream, so cost doesn't grow with palace size over time.

## 4. The dreaming algorithm

Runs per wing, sequentially per company (respects the existing per-company write mutex from `pkg/mempalace/addressing.go`'s `LockCompany`, same as any other palace write).

```
for each company with MempalaceAvailable() and palace InitStatus == "ready":
  for each wing in {project wings, agent wings, company wing}:
    lastRun := latest successful DreamRun for (company, wing)
    watermark := lastRun.WatermarkAfter or wing-creation-time

    newDrawers := mempalace_search or a source_file/created_at range fetch,
                  scoped to wing, created_at > watermark, capped at MAX_DRAWERS (e.g. 300)
    if len(newDrawers) < MIN_ACTIVITY_THRESHOLD (e.g. 5):
        record DreamRun{Status: "skipped-no-activity"}; continue   // no LLM call, no cost

    validFacts := mempalace_kg query for wing (current, non-expired triples)
    existingInsights := mempalace_search scoped to wing, room="insights" (for dedup context)

    candidates := call UtilityModel with a structured-output prompt:
        input:  newDrawers (verbatim, capped/truncated), validFacts, existingInsights
        ask:    "propose 0-5 NEW patterns/preferences/risks, each with a one-line reasoning
                 and the exact evidence items (drawer ids / fact ids) it's based on.
                 Do not restate an existing insight. Do not propose anything with only
                 one piece of supporting evidence unless it's a strong explicit signal
                 (e.g. an explicit stated preference). Prefer silence over speculation."
        output: [] | [{predicate, subject, object, reasoning, confidence, evidence_ids}, ...]

    for each candidate:
        dup := mempalace_check_duplicate against existingInsights (paraphrase threshold 0.80)
        if dup found and not contradicted:
            skip; InsightsSkipped++
        elif dup found and contradicted (opposite claim):
            mempalace_kg_invalidate(old); mempalace_kg_add(new, superseded_reason); 
            write superseding insight drawer; InsightsSuperseded++
        else:
            mempalace_kg_add(new); mempalace_add_drawer(insight); InsightsAdded++

    record DreamRun{Status: "completed", WatermarkAfter: now, ...token counts}
    log MemoryActivity{AgentName: "dreaming", Tool: "dream_cycle", Kind: "maintenance", Wing: wing}
```

Key properties:
- **Bounded cost.** One `UtilityModel` call per active wing per night, input capped at `MAX_DRAWERS`, skip entirely when a wing had negligible activity. For a company with N projects this is N+2 calls/night (projects + company wing + optionally per-agent wings — see §6 on scope), not proportional to total palace size.
- **Idempotent and resumable.** If the job crashes mid-company, `DreamRun.Status="running"` rows are picked up by the same startup-sweep pattern already used for stuck `InitStatus`. `WatermarkAfter` only advances on success, so a crashed run just gets retried, never double-counts.
- **No agent involvement, no run-path latency.** Entirely off the hot path — agents don't know it's happening.
- **Never rewrites Tier 1.** Only ever adds/invalidates Tier 3 (`pattern`/`preference`/`risk`) KG triples and `insights`-room drawers. Never touches operational drawers, decision records, or KG facts written by teardown capture/agents.

## 4a. Audit pass — checking existing memory for staleness, not just proposing new insights

§4 only ever contradicts a dreamed insight against *other* dreamed insights. It never revisits **Tier 2** — KG facts written by agents/engine hooks (`uses`, `blocks`, decision records, …) — so a fact like `(project:acme, uses, "Postgres")` from three sprints ago can sit there uncontested indefinitely, even once new drawers/decisions make it stale. Dreaming should audit, not just add.

**Runs as a second stage of the same nightly cycle, after the propose-new-insights stage in §4, same wing, same `DreamRun`:**

```
staleCandidates := validFacts filtered to:
    - facts older than STALE_AGE_THRESHOLD (e.g. 30 days) since valid_from, OR
    - facts whose subject/object also appears in newDrawers (i.e. touched by recent activity)
    // bounds the audit to facts plausibly affected by what's new — never a full-palace rescan

if len(staleCandidates) == 0:
    skip audit stage; continue to next wing   // no LLM call, no cost

verdicts := call UtilityModel with a structured-output prompt:
    input:  staleCandidates (the existing triples, each with its evidence/provenance),
            newDrawers, validFacts (for cross-checking against sibling facts)
    ask:    "For each existing fact, does the new evidence CONFIRM it (still true),
             CONTRADICT it (evidence says otherwise), or is it UNCHANGED (no bearing
             either way)? Only flag CONTRADICT with specific contradicting evidence —
             absence of mention is not contradiction. Default to UNCHANGED when unsure."
    output: [{fact_id, verdict: "confirm"|"contradict"|"unchanged", reasoning, evidence_ids}]

for each verdict:
    case "confirm":
        bump fact's last_confirmed_at (metadata field) — no KG write; just freshness bookkeeping
    case "contradict":
        mempalace_kg_invalidate(fact, reason=verdict.reasoning, evidence=verdict.evidence_ids)
        write a superseding decision drawer in the `decisions` room (same as the existing
        human/CTO invalidation path in integration plan §3.2) so the audit trail reads the
        same whether a human, an agent, or dreaming did the invalidating
        AuditContradicted++
    case "unchanged":
        no-op
```

This is deliberately **conservative and self-limiting**:
- Only audits facts that are either old *or* topically touched by new activity — never a blanket palace-wide re-verification, so cost stays bounded the same way §4 is.
- Bias toward `unchanged` is explicit in the prompt — an audit pass that over-invalidates is worse than one that under-invalidates, because false invalidation silently erases working knowledge agents may be relying on, while under-invalidation just means the next cycle gets another chance.
- **Never deletes.** Contradiction always goes through `kg_invalidate` (closes the validity window, keeps history) + a superseding decision drawer — identical mechanics to how a CTO/CEO agent invalidates a fact today. Dreaming doesn't get a new, more destructive invalidation path; it uses the existing one.
- Applies to **Tier 2 facts as well as Tier 3 insights** — the audit stage is what lets a stale `(project:acme, uses, "Postgres")` fact eventually get superseded even though no human or agent happened to notice and invalidate it manually. This closes the gap called out in the pilot findings (`mempalace-tech-spec.md` §7.1: "`memory_invalidate` never used" by agents in practice) — dreaming becomes a backstop for invalidation hygiene, not just a source of new facts.
- Contradiction verdicts on **Tier 2** facts are always `unconfirmed`-equivalent for review purposes: log them distinctly (`AuditContradicted` counter, separate from `InsightsSuperseded`) and surface them in the Dreams UI (§7) with higher visual weight than routine Tier 3 supersessions, since invalidating an agent-authored fact is a bigger claim than superseding a prior guess.

## 5. Scheduling

Extends the nightly maintenance scheduler already planned in `mempalace-tech-spec.md` Phase 5 (alongside `compress`/`sync`) — one more job type, not a new subsystem.

- **Cadence:** daily, default off-peak hour (configurable, default `03:00` server time). Per-company, not per-project — iterates that company's wings within one scheduler tick.
- **Company opt-out:** a company-level setting (`Company.DreamingEnabled`, default `true` once memory is available) — some companies may want zero synthesized-memory writes (e.g. compliance-sensitive clients); this is a simple toggle next to the existing memory feature flag in Settings.
- **Manual trigger:** `POST /api/memory/maintenance/dream` (same pattern as the existing `mine`/`sync`/`compress` maintenance endpoints in §4.4 of the integration plan) for on-demand runs (e.g. "dream now" button after a big sprint), reusing the same `DreamRun` bookkeeping.
- **Concurrency:** one dreaming worker across the whole app (like the existing 24h MCP tool-cache job) — companies processed sequentially, not in parallel, to avoid competing with agent runs for LLM provider rate limits and to keep memory-write-lock contention predictable.

## 6. Scope: which wings dream, and how often

Not all wings need nightly dreaming at the same cost/benefit ratio:

| Wing type | Default cadence | Rationale |
|---|---|---|
| `project:<name>` | Daily | Highest signal density — sprints, tasks, decisions, bugs concentrate here. |
| `agent:<name>` | Weekly | Personal-pattern signal (estimation bias, recurring mistakes) accumulates slower; daily would mostly hit the `skipped-no-activity` path anyway. |
| `company` | Weekly | Cross-project patterns need more data to be meaningful; also the highest-blast-radius wing if a bad insight leaks — lower cadence gives more time for human review before it compounds. |

Cadence per wing type is a config constant, not per-wing state — simple to reason about, easy to tune later from real `DreamRun` cost/yield data.

## 7. Review, trust, and pruning

Because insights are inferred, not stated, they need a lighter-weight but real review loop — this is the main new surface area beyond "add a scheduled job":

1. **Recall-time weighting.** When `recall_memory`/`memory_facts` return a Tier 3 insight, the response is tagged `review_state` and `confidence` so the calling agent's prompt instructions can be explicit: *"pattern/preference/risk facts are inferred, not confirmed — treat as a hint, verify before relying on it for anything consequential."* This mirrors the existing "KG wins over drawer text" epistemic-ordering rule (integration plan §3.2), extended with a third tier below KG-confirmed facts.
2. **Promotion.** A planner-tier agent (CEO/CTO) or a human via the Memory UI can promote an `unconfirmed` insight to `confirmed` — at which point it's treated like any other KG fact in ranking/prompting. No automatic promotion; confidence score from the LLM is a hint for the UI to sort by, not a threshold that self-promotes.
3. **Rejection / pruning.** Humans can mark `rejected` (kept for audit, excluded from recall) or hard-delete via `mempalace_delete_by_source` on the `dreaming/<run-id>/...` prefix — cleans both the KG triple and its companion drawer in one action, reusing the source_file-scoped delete already designed for run cleanup.
4. **Bounded growth.** Cap unconfirmed insights per wing (e.g. 50); when a new one would exceed the cap, the lowest-confidence unconfirmed insight older than N days is auto-pruned rather than accumulating forever. Confirmed insights are never auto-pruned.
5. **Audit contradictions get their own review queue.** Because §4a can invalidate a Tier 2 fact that an agent or human wrote deliberately, an `AuditContradicted` invalidation is surfaced separately from routine Tier 3 supersession — a "Contradictions" filter in the Dreams tab, sorted by fact age/importance rather than confidence. A **revert** action is available for exactly this case (distinct from Tier 3 reject/prune): it re-opens the invalidated fact's validity window and marks the superseding decision drawer `reverted` — cheap because `kg_invalidate` never deleted anything, it only closed a window.
6. **Memory UI — new "Dreams" tab** (extends the Memory UI plan in integration-plan §4.4): list of insights per wing with confidence, evidence links (click through to the source drawers/facts that produced it), review-state filter, and promote/reject actions, plus the Contradictions sub-view from point 5. `DreamRun` history (cost, counts, skipped/errored nights, `AuditConfirmed`/`AuditContradicted`) as a small ops sub-view — reuses the `Activity & Stats` tab pattern already planned.

## 8. Cost and safety guardrails

- **Model:** the company's configured cheap/utility model (`LLMProvider` + `UtilityModel`, same resolver already used elsewhere in the engine) — never the primary agent model. Dreaming is explicitly a background-batch use case, not latency-sensitive.
- **Token caps:** hard cap on input (`MAX_DRAWERS` × truncation-per-drawer) and output (max 5 candidates per call, each capped in length) — bounds worst-case cost per wing per night regardless of activity volume.
- **Prompt-injection awareness.** Dreamed input includes verbatim drawer content, which can include agent/tool output or even external file content (already a known risk vector per MemPalace's own README, which itself contains what reads like an injection attempt). The dreaming prompt must explicitly instruct the model to treat drawer content as *data to analyze*, never as instructions to follow, and structured-output-only responses (no free-form tool calls available to the dreaming call) close off any actual exploit surface — the dreaming LLM call has no tools, so even a successful injection can only corrupt what it writes into the `insights` room, which is itself sandboxed behind the review/promotion gate in §7.
- **Failure mode:** any error (LLM call fails, palace unreachable) → `DreamRun.Status="error"`, logged, watermark unchanged, silently retried next night. Never blocks or degrades the rest of the app — same soft-failure philosophy as the rest of the MemPalace integration.

## 9. Implementation phasing

Slots in after `mempalace-tech-spec.md` Phase 2 (compaction) and reuses its plumbing (KG invalidation/supersede helpers, addressing, write lock, `MemoryActivity` logging) — no new foundational work needed beyond what Phases 0–2 already establish.

**Phase D0 — Core job, project wings only, manual trigger (~3-4 days)**
- `DreamRun` table + migration (including the `AuditConfirmed`/`AuditContradicted` counters).
- `pkg/mempalace/dreaming.go`: propose-new-insights stage (§4) **and** the audit/invalidation stage (§4a), scoped to `project:<name>` wings only.
- `POST /api/memory/maintenance/dream` manual trigger. No scheduler yet — prove both stages and tune prompts/caps/thresholds against real palace data first. Shipping the audit pass alongside the propose pass in the same phase, rather than deferring it, is deliberate: they share the same read/write plumbing and the "bias toward `unchanged`" prompt tuning in §4a needs the same real-data pass as §4's dedup tuning.

**Phase D1 — Scheduling + remaining wing scopes (~1-2 days)**
- Nightly scheduler integration (§5), `Company.DreamingEnabled` toggle.
- Extend scope to `agent:*` and `company` wings with their cadences (§6).

**Phase D2 — Review UI + recall integration (~2-3 days)**
- "Dreams" tab in Memory UI (§7.6), including the Contradictions sub-view + revert action (§7.5).
- `recall_memory`/`memory_facts` response tagging + epistemic-ordering prompt update.
- Promote/reject/prune actions wired to KG invalidate + `delete_by_source`; revert action wired to KG re-open-validity-window.

**Phase D3 — Tuning pass (~ongoing)**
- Once real `DreamRun` cost/yield data exists: adjust `MAX_DRAWERS`, `MIN_ACTIVITY_THRESHOLD`, `STALE_AGE_THRESHOLD`, per-wing-type cadence, and confidence-threshold defaults for auto-pruning. This is expected to need at least one real tuning iteration — ship conservative defaults (fewer, higher-confidence insights; audit biased toward `unchanged`) first.

**Total: roughly 1–1.5 weeks**, ahead of the pruning/tuning pass which is open-ended by nature.
