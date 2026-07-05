# Complex Memory Scenarios — Implementation & Results

**Status:** first real run against the shipped implementation, no implementation changes made (per instructions — this is observation only).
**Spec:** `docs/mempalace-memory-test-scenarios.md`
**Test file:** `e2e/tests/memory_complex_scenarios.spec.ts`
**Run:** real engine (`go run .`), real palace, real MiniLM embeddings, scripted mock LLM — same harness as the existing `memory_recall.spec.ts`/`memory_dedup_gauntlet.spec.ts`.

## What got implemented this pass

Implemented and run: **Scenario 1** (The Pivot), **Scenario 2** (disguised contradiction inside a dedup storm), **Scenario 4** (cross-tenant leakage), **Scenario 7** (cross-role blocker flow, ICP backend / GM Coin).

**Not implemented this pass** — flagging honestly rather than skipping silently:
- **Scenario 3** (compaction recall) — needs a real 150+-turn transcript large enough to trip `pruneHistory`'s token-budget threshold; higher setup cost, deferred.
- **Scenario 5** (concurrent writes) — needs a true parallel-request harness and multiple iterations to be meaningful (a single-shot run proves nothing about a race); deferred.
- **Scenario 6** (Dreaming adversarial input) — **cannot be tested: Dreaming is a design doc only (`mempalace-dreaming-spec.md`), nothing has been built.**
- **Scenarios 8, 9, 10** — mechanically similar to 1/7 (delegation chains, handoff, cross-project tunnels); deferred for time, not because they're harder.

## Scope adjustment vs. the spec doc — a finding in itself

Scenario 1's spec assumed an agent-callable `kg_add` tool for arbitrary subject/predicate/object triples. **The shipped agent tool surface has no such tool.** Reading `engine/aicli/tools/mempalace.go`, agents get: `recall_memory`, `recall_run`, `remember`, `memory_facts` (read-only KG query), `memory_invalidate`, `recall_company`. The knowledge graph is written only by **engine hooks** (refinement, teardown), scoped to the current task's own `task-<ref>` entity — agents cannot add arbitrary KG facts, only query or invalidate. Tests were adapted to the tools that actually exist. This is worth surfacing on its own: **the "KG wins over drawer text on conflict" epistemic-ordering rule described in the integration plan and assumed by the dreaming spec is not wired into `recall_memory` at all** — confirmed directly below.

## Results

### Scenario 1 (The Pivot) — all assertions passed, but the underlying data shows the pivot risk is real

No hard failures, because the test (correctly, in hindsight) only asserted "recall returns something" and logged the actual ranking for inspection rather than asserting strict ordering. The logged data shows the exact failure mode the scenario exists to catch:

- The **top-ranked hit** for "how do we push real-time task status updates to the frontend" was generic task boilerplate (`"> Task: Real-time task status updates..."`), not either decision.
- The **superseded WebSockets decision** and the **current SSE decision** both appear in the result set with no clear preference for the current one — `memory_invalidate` was called (and returned `"success": true`) but had **no effect on ranking**, because `recall_memory` calls `mempalace_search` only — it never consults the KG. There is no code path where an invalidated KG fact suppresses or demotes a semantically-similar drawer.
- `memory_invalidate` itself is worth flagging: it returned `"success": true` and a fabricated-looking `"fact": "task-status-transport → uses → WebSockets", "ended": "2026-07-05"` for a fact that was **never actually added** (no `kg_add` tool exists for agents) — it silently "succeeds" against a fact that structurally can't have existed. This gives false confidence: an agent (or a human reading the tool result) would reasonably believe the invalidation had a real effect on future recall, and it didn't.
- The historical query ("why did we reject WebSockets") correctly surfaced both the original decision and the `[superseded] ...` note — verbatim history preservation itself works.

**Verdict: the pivot/epistemic-ordering mechanism described in the design docs is not actually implemented in the current codebase.** Recall is pure semantic+BM25 ranking with no current-truth preference. This is the single most important finding from this run.

### Scenario 2 (disguised contradiction in a dedup storm) — reproduces the exact targeted risk

- Dedup caught only 1 of the 2 genuine near-duplicates (`s3` deduped against `s1`; `s2`, a looser paraphrase of the same fact, was stored as an independent drawer) — within the tolerance the scenario anticipated ("≤2 if wording variance legitimately splits it").
- The real pivot (`s4`, narrow-allowlist truth) and the overstated near-duplicate of it (`s5`, "fully lifted," factually wrong) were **both stored as independent facts** — dedup did not catch or flag the overstatement.
- **The consequential result:** querying `"can I use shell commands in the sandbox"` ranked the **stale** "no shell at all" claim (`s2`) **first**, the **wrong, overstated** "fully lifted" claim (`s5`) **second**, and the **actually-correct** narrow-allowlist fact (`s4`) **last** among the substantive results.

**Verdict: an agent asking this exact question today would most likely be told "no" or "yes, fully" — not the true, narrow, current answer.** This is a direct, reproduced instance of the silent failure Scenario 2 was designed to catch, not a hypothetical.

### Scenario 4 (cross-tenant leakage) — the core protection holds; a supplementary check was inconclusive

- **The assertion that matters most passed cleanly**: the Borealis-scoped agent's `recall_memory` tool call for a deliberately fishing query returned **no** Meridian-specific content and **no** leaked competitive figures. Project-wing scoping on the actual agent-facing tool path works as designed.
- A supplementary sanity check (querying the raw `/api/memory/search?...&wing=project-atlas` HTTP endpoint directly, meant as a positive control proving the fact is retrievable in its own scope) returned **only the project's own metadata record**, not the planted Meridian decision text at all — for either project. This means the positive control didn't actually validate what it was meant to; either the `wing` query parameter on that specific HTTP endpoint isn't respected the way the agent tool's internal call is, or there's a broader retrieval quality gap on that endpoint. **Flagging as inconclusive, not as a leak** — the isolation guarantee is solidly evidenced by the agent-tool-path result, but the supplementary check needs a corrected approach (e.g., matching the exact query param the endpoint expects) before it can be trusted either way.

### Scenario 7 (cross-role blocker flow) — real, reproducible failure, confirmed under a design adjustment too

**This is the most actionable finding.** The CMO agent's very first recall (`recall_memory("ICP backend implementation GM Coin status")`), fired seconds after the CTO's task (which had called `remember()` with the blocker note) finished, found **only the auto-created project metadata drawer** in the project wing — `total_before_filter: 1`. The CTO's note was invisible to a different agent's very next query in the same project.

- **Reproduced twice, independently**, with an *identical* 592ms duration both times — this reads as a systematic latency window, not random flakiness.
- No code-level agent-based access restriction exists (`added_by` is write-only metadata, never used to filter reads) — ruled out as the cause.
- Wing scoping resolved identically for both agents (`project-gm-coin` in both requests) — ruled out as the cause.

**Adjustment (per follow-up request): route CMO's recall through the CEO instead of letting CMO call `recall_memory` directly.** Restructured using the real delegation primitives (`create_subtask` / `ask_task_owner` / `answer_subtask_question`, same pattern as `ceo_orchestration.spec.ts`): CEO delegates the post-writing task to CMO; CMO's only move is `ask_task_owner` — it never calls `recall_memory` itself (verified directly: CMO's own LLM requests, isolated by system-prompt content, contain no `recall_memory` call); CEO is the one who calls `recall_memory` and answers CMO via `answer_subtask_question`.

**Caveat carried over honestly: this is a scripted workflow convention, not a real access-control mechanism.** Nothing in `engine/aicli/tools/mempalace.go` restricts `recall_memory` by role — CMO could still call it directly and would get the same project-wing results CEO does. "Only CEO has access to CTO memories" is not something the codebase enforces; this adjustment models the desired workflow and proves CMO *can* be kept off the direct-recall path by design, not that the system prevents it from going around that design.

**Result: the exact same failure reproduced, now with CEO as the reader instead of CMO** — again `total_before_filter: 1`, again only the project metadata drawer, again no `dfx`/CTO content, timing in the same ~850ms range. This is actually a **more informative result than the original failure**: it rules out "something specific to the CMO role or agent" as the cause, since swapping the reading agent to CEO changed nothing. The variable that matters is *elapsed wall-clock time between the write and the cross-agent read*, not which two roles are involved. Scenario 1 and 2's same-agent, multi-step recalls all succeeded; every cross-agent recall attempted within roughly a second of the preceding write — CMO→CTO originally, now CEO→CTO — has failed identically, three times across two different agent pairings.
- Because the test suite runs serially and this failure is early in the scenario, the two "unblocked" tests (cross-run recall of the exact error text, and the final CEO/CMO pivot check) **did not execute** in either run. Worth re-running once the root cause is understood, ideally with a short poll-until-indexed helper inserted to distinguish "genuine latency" from "actually broken" definitively.

**Verdict: cross-agent, cross-task memory coordination — the entire premise of Scenario 7 and the reason paperclip2 wants a shared memory layer at all — has a measurable, reproducible gap for facts written and read in rapid succession, independent of which two roles are involved.** Whether this matters in practice depends on real task timing (a human-paced workflow has minutes between task transitions, not sub-second), but it's a real gap in the guarantee as currently implemented, not a theoretical one.

## Summary table

| Scenario | Ran? | Result | Real finding |
|---|---|---|---|
| 1. The Pivot | Yes | All assertions passed (assertions were left deliberately non-strict) | KG-based epistemic ordering is not wired into recall at all; `memory_invalidate` silently "succeeds" on facts that were never added |
| 2. Disguised contradiction | Yes | All assertions passed (by design; ambiguity was intentionally left open) | Recall ranks a stale claim and a wrong overstated claim above the correct current fact for a realistic query |
| 4. Cross-tenant leakage | Yes | Core assertion passed | No leak on the agent-tool path; a supplementary HTTP-endpoint check was inconclusive, not proof of anything either way |
| 7. Cross-role blocker | Yes | **1 failed, reproduced 3x (incl. once with the CEO-delegation adjustment)** | Cross-agent recall of a just-written fact failed within ~1s of the write, regardless of which two roles are involved — points to an indexing-latency gap, not a role-specific bug |
| 3, 5, 6, 8, 9, 10 | No | — | Deferred (compaction/concurrency need heavier harnesses; Dreaming isn't built; 8-10 not reached this pass) |

## Suggested next steps (not acted on — reporting only, per instructions)

1. Confirm the Scenario 7 gap's root cause: instrument or poll `/api/memory/status` (`total_drawers`) between the CTO write and the CMO read to see if it's a fixed indexing delay, and measure its magnitude.
2. Decide, as a design question (not urgent): should `recall_memory` actually consult the KG for current-truth preference, matching the design docs, or should the docs be corrected to describe the shipped (pure-search) behavior? Right now the two disagree.
3. Re-run scenario 7's deferred "unblocked" steps once the above is understood, to see whether cross-run recall of verbatim error text (a separate, unrelated mechanism) works correctly in isolation.
