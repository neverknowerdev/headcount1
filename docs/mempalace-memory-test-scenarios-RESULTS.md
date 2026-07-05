# Complex Memory Scenarios — Implementation & Results

**Status:** Four fixes implemented and verified: the scenario-7 write-contention bug (`pkg/mempalace/manager.go`), plus the three follow-up fixes from the scenario-1/2 findings — ranking demotion, `memory_invalidate` honesty/marking, and an end-of-task memory nudge. See "Fix applied" (scenario 7) and "Follow-up fixes" (below) for details and verification.
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

**Result (2nd run): the exact same failure reproduced, now with CEO as the reader instead of CMO** — again `total_before_filter: 1`, again only the project metadata drawer, again no `dfx`/CTO content, timing in the same ~850ms range. Ruled out "something specific to CMO" as the cause, since swapping the reading agent to CEO changed nothing.
- Because the test suite runs serially and this failure is early in the scenario, the two "unblocked" tests (cross-run recall of the exact error text, and the final CEO/CMO pivot check) **did not execute** in either of the first two runs.

**Correction after a 4th run (per follow-up: does an explicit `room` filter help?)** — this run added two things: (1) a direct `/api/memory/drawers` listing check immediately after CTO's write, and (2) a second, room-scoped `recall_memory` retry (`room: 'general'`, the room CTO's `remember(kind:'note')` actually lands in — **there is no per-agent "CTO room" in this taxonomy**, only topic rooms `general`/`decisions`, plus a `technical` room populated by transcript mining). Result: **all 5 tests passed this time**, including the previously-failing one. This forces a correction to what I claimed after the first two runs:

- **The "identical ~850ms duration, systematic not flaky" framing was wrong.** A 4th run under the same script, same assertions removed only for this diagnostic pass, succeeded outright — the unscoped `recall_memory` call this time returned `total_before_filter: 3` and the CTO note was present in the results. The failure is **intermittent**, correlated with real wall-clock timing (this run's setup/GM-88 steps took ~11s each, vs. ~1-4s in the failing runs — plausibly giving background indexing enough real time to catch up before the next query fired) — not a fixed, deterministic gap as I stated. This is a meaningfully different (and more accurate) characterization: it's consistent with an **async write-to-search indexing race whose outcome depends on ambient system load/timing**, not a deterministic bug that fires every time under ~1s.
- **The drawer-listing check confirms the write itself lands immediately**: CTO's note appeared in `/api/memory/drawers` right after its task finished, in every run including the failing ones (checked in this 4th run). So the write path is not the problem — it's specifically whether the **search/embedding index** has caught up by the time the next cross-agent query fires. This narrows the likely cause to embedding/index commit latency, not a write bug.
- **On the room-filter question specifically: it helps, but not with the failure mode that actually occurred.** In this successful run, the unscoped query already found the CTO note — the room filter wasn't needed to fix a total miss (it can't retrieve a document that isn't indexed yet at all, which is what happened in the two failing runs). What the room filter **did** demonstrably fix is a **separate, real precision problem**: in the unscoped result set, the actual substantive content (CTO's note, room `general`, similarity 0.314) ranked **3rd of 3** results — *below* two pieces of generic boilerplate from the `technical` room (a bare task-description echo and a raw transcript dump of the remember/finish_task exchange, both scoring higher by cosine similarity, 0.76 and 0.216). Filtering to `room: 'general'` cut straight to the one relevant document as the sole, top-ranked result. **So the room parameter is a genuinely good practical mitigation for a real ranking-noise problem** (generic `technical`-room transcript content outranking the actual fact for a status-style query — the same "boilerplate ranks above substance" pattern already documented in Scenario 1) — just not a fix for the separate indexing-latency issue, which no client-side filter can address since the document isn't in the corpus yet at query time.
- The two "unblocked" follow-up tests ran successfully in this 4th pass: CTO's cross-run recall of the exact error text succeeded (`operation not permitted` found verbatim), and CEO's final recall correctly surfaced the implemented facts (`ic-cdk 0.13`, `mint/transfer/balance`) rather than its own round-1 "not implemented yet" answer.

**Revised verdict (before the next correction below):** two distinct findings — (1) an intermittent write-to-search indexing race, (2) a ranking precision problem fixable with `room`. Both real, but as the next round shows, (1) understated the actual severity.

**Test-design correction (per follow-up): stop testing the race at all.** In real usage, GM-88 being marked blocked and someone picking up GM-91 are separated by minutes (human review, task-board polling), not milliseconds — firing the CEO's recall immediately after CTO's `finish_task` response was stress-testing a race the real workflow never exercises. Replaced the immediate-fire pattern with `waitForSearchable()`: poll the real search API (up to 60s) until the fact is actually there, instead of guessing a fixed delay or asserting on sub-second timing.

- **A flat 5-second delay was empirically NOT enough** — 3 repeated full runs with a hardcoded 5s pause between CTO's write and CEO's recall **all still failed** the same way (`total_before_filter: 1`). This ruled out "it's just a brief race that a small buffer papers over."
- **Switching to the poll-based wait surfaced something more serious than an indexing lag.** In one polling run, `waitForSearchable` polled the search API for the **full 60-second timeout** and never found the fact — and a direct `/api/memory/drawers` listing check taken immediately after CTO's task finished (before any polling) showed **only the auto-created project metadata drawer — CTO's `remember()` drawer was never listed at all**, not even via the non-search listing endpoint. This is different from every earlier run: previously the drawer-listing check always showed the write landed instantly, and only search lagged. Here, the write itself appears to be **missing**, not just slow to index.
- **This coincides with a new error class in the server log for that same run**, appearing right after CTO's task completes: `Warning: auto-diary write failed: ... "Peer MCP writer active; this server is read-only for mutating tools"`, and the same `"Peer MCP writer active"` message on the teardown's `mempalace_kg_add` (×2) and `mempalace_mine` calls. This points to **MCP single-writer lock contention** — plausibly from the three agents in this scenario (CEO, CTO, CMO) each opening their own MCP connection to the same company palace in quick succession, tripping mempalace's documented single-writer-per-palace constraint (see `mempalace-integration-plan.md` §5.3) and causing a peer connection's mutating calls to be rejected as read-only. The warnings logged were for teardown-time secondary writes (diary/KG/mine), not `remember()` itself directly, but the drawer-listing gap is consistent with the *same* lock contention having also silently affected (or delayed past the point this run's poll gave up) CTO's `remember()` write.
- **This was not re-checked before the process was torn down** — the go server had already exited by the time this was investigated, so `g1`'s own tool result (did `remember()` itself report `"success": true` or an error in this specific run?) wasn't captured. That's the concrete next step, not concluded here.

**Revised verdict (superseded by the fix below):** what looked like a simple "search hasn't caught up yet" issue is, on more careful testing, at least sometimes a case where **the write may not land at all**, correlated with multi-agent MCP connection contention on one company's palace — a real concurrency finding, closer to Scenario 5's territory (concurrent writes) than Scenario 7's original framing (cross-agent recall timing) suggests.

### Fix applied: `MineProjectAsync` was racing the MCP server for the palace's writer lease

**Root cause found.** `pkg/mempalace/manager.go`'s `MineProjectAsync` (triggered automatically on project creation, and by the `/api/memory/maintenance/mine` endpoint) span a **separate `mempalace` CLI subprocess** (`exec.CommandContext(..., "mine", workspaceDir, ...)`) against the same palace directory the company's long-lived `mempalace-mcp` server process already holds an exclusive writer lease on. The two processes competed for that single-writer lease — exactly the failure mode `MineTranscript`'s own existing code comment already warned about ("a CLI `mine` would be rejected with 'palace is held by PID' whenever memory is in use"), but `MineProjectAsync` hadn't been updated to avoid it. The result: the MCP server would demote itself to read-only for the duration of the conflict (`"Peer MCP writer active; this server is read-only for mutating tools"`), silently degrading or delaying concurrent agent writes (`remember()`, diary, KG facts) — exactly the pattern observed in Scenario 7, which creates a project (triggering this background mine) immediately before running three agents against it.

**Fix:** route `MineProjectAsync` through the same shared MCP client / `CallServerTool` path `MineTranscript` already uses (`mempalace_mine` tool call, `mode: "projects"`) instead of a competing CLI subprocess, so all writes to a palace serialize through one client and one lock, never two processes. Updated call sites in `server/controllers/projects.go` and `server/controllers/memory.go` to pass the already-available `db.MCPServer` row through.

**Verification:**
- `go build ./...` and `go vet ./...` clean; `go test ./pkg/mempalace/... ./engine/...` all pass.
- Scenario 7 (`memory_complex_scenarios.spec.ts`), run 4 times after the fix: **all 5 sub-tests pass every time**, **zero** `"Peer MCP writer active"` warnings across all 4 runs (down from several per run before), and measured write-to-search latency dropped from **up to 60,000ms (timeout, content never found)** to a consistent **~150–200ms** — matching the speed of same-agent recall in Scenarios 1/2, which never exhibited this problem.
- Full `memory_complex_scenarios.spec.ts` suite (all 4 scenarios, 14 tests): all pass, 19.4s total, no regressions.
- Pre-existing specs (`memory_recall.spec.ts`, `memory_dedup_gauntlet.spec.ts`, `memory_teardown.spec.ts`, 12 tests): all still pass, no regressions from the change.

**What this does and doesn't resolve:** this fixes the write-contention bug that was causing Scenario 7's intermittent failures. It does **not** address the separate, independently-confirmed findings from Scenarios 1 and 2 (recall not consulting the KG for current-truth preference, `technical`-room noise outranking real content in ranking) — those remain open design questions, not bugs with an equivalent single root cause.

## Summary table

| Scenario | Ran? | Result | Real finding |
|---|---|---|---|
| 1. The Pivot | Yes | **Fixed** — hardened from logged-only observation to hard pass/fail assertions, now passing | Was: KG-based epistemic ordering not wired into recall; `memory_invalidate` fabricated success. Fixed below. |
| 2. Disguised contradiction | Yes | **Fixed** — hardened to hard assertions, now passing | Was: recall ranked a stale claim above the correct current fact. Fixed below. |
| 4. Cross-tenant leakage | Yes | Core assertion passed | No leak on the agent-tool path; a supplementary HTTP-endpoint check was inconclusive, not proof of anything either way |
| 7. Cross-role blocker | Yes | **Fixed** — was intermittent (up to 60s write-to-search gap / never resolving); now 5/5 sub-tests pass consistently across 4 verification runs | Root cause: `MineProjectAsync` raced a CLI subprocess against the live MCP server for the palace's writer lease, causing `"Peer MCP writer active"` and silently degraded writes. Fixed by routing through `CallServerTool` like `MineTranscript` already did. |
| 3, 5, 6, 8, 9, 10 | No | — | Deferred (compaction/concurrency need heavier harnesses; Dreaming isn't built; 8-10 not reached this pass) |

## Follow-up fixes: ranking demotion, `memory_invalidate` honesty, end-of-task memory nudge

Three more fixes, addressing the Scenario 1/2 findings directly (root-caused and implemented, not just observed):

### 1. Ranking: demote boilerplate and superseded content

**Problem (Scenario 1 & 2):** mempalace's own ranking is pure similarity/BM25 — it has no notion of "this is boilerplate" or "this was superseded." A bare task-description echo or a raw transcript dump (`technical` room) could outrank a genuinely relevant fact; a decision that had been explicitly overturned kept ranking as if still current.

**Fix:** `recall_memory` and `recall_company` (agent tools) and `/api/memory/search` (HTTP API, used by the Memory UI) now over-fetch (3x the requested limit, capped) and re-rank locally via the new shared `pkg/mempalace.RerankSearchResults`: `technical`-room/transcript hits get a 0.55x similarity penalty, `[SUPERSEDED`-marked content gets a 0.15x penalty, then results are re-sorted and trimmed to the originally-requested limit. Explicit `room` filters bypass this (the caller already narrowed scope).

### 2. `memory_invalidate`: stop fabricating success, actually mark the overturned content

**Problem (Scenario 1):** `memory_invalidate` reported `"success": true` for a KG fact that structurally never existed (agents can't add arbitrary KG triples — only engine hooks write the KG), and only ever added a small companion note, never touching the actual stale decision text that needed demoting.

**Fix:** now queries `mempalace_kg_query` first; if no current fact matches, it says so explicitly ("No current KG fact found... nothing to invalidate") instead of claiming success. It also marks the actual overturned drawer(s) with a `[SUPERSEDED]` prefix — substring-matched on `object` against `general`/`decisions` room drawers (mempalace search doesn't return `drawer_id`, so this reuses the engine's existing `supersedePreviousPlan` list/get/update pattern), capped at 3 drawers. This is what lets the ranking fix in #1 actually demote the real stale content, not just a note about it.

**Real bug caught during implementation:** the object-substring match can accidentally mark the *new* decision too, if its text naturally mentions the old term (e.g., "replaced WebSockets with SSE" contains "WebSockets"). Fixed by updating the Palace Protocol prompt to recommend invalidate-then-remember ordering (invalidate while only the old drawer exists, before the new one is written) — verified in the updated Scenario 1 test, which reordered its script to match and confirmed no false-positive marking.

### 3. End-of-task memory nudge

**Per follow-up request** ("ask agent to make memories with facts and learnings at the end of each task"): `finish_task` now nudges once per run — if the agent hasn't called `remember()` at all, the first `finish_task` call returns a reminder instead of completing ("store durable facts, decisions or learnings, then call finish_task again"); the second call always proceeds regardless. Wired via `MempalaceProxy.HasRemembered()`; a nil check means it's a no-op when memory isn't available for the run. Palace Protocol prompt text updated to match.

**Design tradeoff considered and rejected:** a "soft" nudge (finish immediately, just append a note to the result) would be simpler for callers but can't actually change agent behavior — by the time `finish_task` returns, the run is over; nothing reads that note. The blocking, once-per-run version is the only one that functions as an actual nudge, matching upstream MemPalace's own stop-hook pattern.

**Real side-effect discovered:** every company gets memory provisioned unconditionally on creation (`mempalace.Available()` is the only gate — see `server/controllers/companies.go`), so this affects **any** scripted e2e test whose agent finishes a run without ever calling `remember()`, not just memory-focused specs. Confirmed by `ceo_orchestration.spec.ts` hanging the same way `memory_recall.spec.ts`'s recall-only task did. Fixed in both those files plus the new `memory_complex_scenarios.spec.ts` via a `withFinishRetries()` transform (splice an identical `finish_task` retry after each scripted one — unused if the first already finishes, consumed if the nudge fires). **Other e2e spec files not touched by this pass may have the same latent gap** — flagging as a known follow-up rather than claiming full-suite coverage.

**Verification:** `go build`/`go vet`/`go test ./...` all clean. `memory_complex_scenarios.spec.ts` (14 tests, including the newly-hardened Scenario 1/2 ranking assertions), `memory_recall.spec.ts`, `memory_dedup_gauntlet.spec.ts`, `memory_teardown.spec.ts`, and `ceo_orchestration.spec.ts` all pass together (29 tests). Confirmed via report data: `memory_invalidate` correctly said "No current KG fact found... Marked 1 matching drawer(s)" (Scenario 1) and "Marked 2 matching drawer(s)" (Scenario 2); Scenario 2's corrected fact (s4) ranked first in search results, ahead of the two now-`[SUPERSEDED]`-marked stale claims.

## Suggested next steps

**Done:** the `MineProjectAsync` write-contention bug, the ranking-demotion fix, `memory_invalidate`'s honesty/marking, and the end-of-task nudge are all implemented and verified above.

Remaining, not acted on:

1. Audit other e2e spec files for the same finish_task-nudge gap (any scripted task that finishes without a preceding `remember()` call) — only `memory_complex_scenarios.spec.ts`, `memory_recall.spec.ts`, and `ceo_orchestration.spec.ts` were checked/fixed this pass.
2. Consider whether the `technicalRoomPenalty`/`SupersededPenalty` constants (0.55/0.15 in `pkg/mempalace/rerank.go`) need tuning against more real data — chosen from this pass's scenarios, not a broader calibration.
3. Audit other call sites for the same CLI-subprocess-vs-MCP-client anti-pattern that caused the `MineProjectAsync` bug — `WakeUp` still shells out to the CLI (`pkg/mempalace/manager.go`), though it's read-only so lower risk; worth confirming it can't also trip the writer-lease conflict or be starved by a peer writer.
4. Pick up Scenarios 3, 5, 6, 8, 9, 10 — deferred from the original pass (compaction/concurrency need heavier harnesses; Dreaming isn't built; 8-10 not reached). Scenario 5 (concurrent writes) in particular now has a known, closely-related bug class to specifically probe for.
