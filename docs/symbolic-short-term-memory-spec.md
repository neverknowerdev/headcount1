# Tech Spec: Symbolic Short-Term Memory (Tool-Result Offloading)

**Status:** Proposed
**Scope:** Intra-run context compaction, *plus* wiring so Hindsight ingests the full, uncompacted run transcript rather than the outcome-only summary it gets today. Long-term memory (retention policy, mental models, recall, vector storage) stays owned by Hindsight (`pkg/hindsight/`) — this spec deliberately excludes the L3 persona/scenario layers and cross-session recall from TencentDB-Agent-Memory. It changes *what raw material* Hindsight receives, not how Hindsight decides what to keep.

## 1. Background

[TencentDB-Agent-Memory](https://github.com/TencentCloud/TencentDB-Agent-Memory) introduces a *symbolic short-term memory* for agent loops: instead of letting verbose tool results accumulate in the LLM context, it

1. **Offloads** each heavy tool result to an external file (`refs/*.md`), keyed by an index entry,
2. **Summarizes** it with a cheap LLM call ("L1"): one-line summary + a 0–10 *replaceability score*,
3. **Condenses** the task's progress into a compact **Mermaid task canvas** ("L2") whose nodes carry `node_id`s that map back to the offloaded files,
4. **Injects** only the Mermaid canvas (a few hundred tokens) into context, and lets the agent **retrieve** any full result on demand via its `node_id`.

Their reported numbers: up to 61% token reduction and +51% relative pass-rate on WideSearch. The mechanism is a superset of what our `pruneHistory` (`engine/aicli/agent.go:455`) already does crudely (age-based head-truncation of stale tool results). This spec re-implements the short-term part natively in Go.

### How TencentDB does it (reference behavior)

- **Hook point:** an `after-tool-call` hook. Tool results are buffered as `ToolPair{toolName, toolCallId, params, result, error, timestamp, durationMs}`; heartbeat/approval-pending results are skipped.
- **Index:** a per-session JSONL (`offload.{session}.jsonl`) of `OffloadEntry{timestamp, node_id, tool_call, summary, result_ref, tool_call_id, score}`. `node_id` is `null` until L2 runs; the raw result lives at `result_ref` (`refs/<timestamp>.md`).
- **L1 trigger:** ≥4 pending pairs force a summarize call. On LLM failure it degrades to `[L1 degraded] <tool>: <truncated result>`.
- **L2 trigger:** ≥4 index entries with `node_id == null`, or 300s since the last L2 run. The LLM receives the existing `.mmd` content + new entries + the latest user turn, and returns updated Mermaid + a `tool_call_id → node_id` mapping, which is back-filled into the JSONL.
- **Canvas format:** a flowchart with JSON metadata in a Mermaid comment (`%%{ taskGoal, createdTime, updatedTime }%%`), nodes like `001-N3["fetched pricing page<br/>status: done"]`, statuses `done | doing | todo`.
- **Injection:** exactly one synthetic user-role message wrapping the canvas in `<current_task_context>…</current_task_context>`, placed *after the latest real user message* (between the question and the tool loop), never splitting a tool_use/tool_result pair. A content fingerprint avoids redundant re-injection; budget capped at `mmdMaxTokenRatio` (0.2) of the context window.
- **Compaction tiers**, evaluated against estimated context tokens vs. the model window:
  - *Mild* (≥0.5): replace low-score stale tool results with their L1 summaries in place.
  - *Aggressive* (≥0.85): delete oldest low-relevance tool call/result pairs entirely; the canvas preserves the trail.
  - *Emergency* (≥0.95 → compact down to 0.6): forcibly drop messages, keeping a minimum floor, when aggressive stalls.
- A cheap heuristic token estimate gates the expensive full count (skip when clearly under 85% of the mild threshold).

## 2. What we build

A new package **`pkg/shortterm/`** plus surgical changes in `engine/aicli/`. Four components:

```
tool result ──► OffloadStore (refs/*.md + index.jsonl)          [deterministic]
            └─► Summarizer (L1, utility model, async)            [LLM]
index ─────────► CanvasBuilder (L2 Mermaid, utility model, async)[LLM]
history ───────► Compactor (tiered, replaces pruneHistory)       [deterministic]
                 + canvas injection + `recall_tool_result` tool
```

Everything is per-run: state lives under the existing per-run log directory and dies with the run. One thing *does* touch Hindsight: at run end, the canonical (uncompacted) history plus the offload store are used to feed Hindsight the full transcript, not just the outcome summary — see §2.6.

### 2.1 Offload store (`pkg/shortterm/store.go`)

Deterministic capture of every sizable tool result, done synchronously inside `Agent.executeToolCalls` (`engine/aicli/agent.go:315`) right where the result string is produced.

- **Location:** `{runLogDir}/offload/` (the dir already created per root run, `native_engine.go:414`):
  - `refs/{tool_call_id}.md` — full raw result, written as
    ```markdown
    **Tool:** {toolName}
    **Call ID:** {toolCallId}
    **Args:** {compact JSON, capped}

    **Result:**
    ```{result}```
    ```
  - `index.jsonl` — append-only, one `Entry` per offloaded call.
- **Types:**
  ```go
  type Entry struct {
      Timestamp  time.Time `json:"timestamp"`
      NodeID     string    `json:"node_id"`      // "" until L2 assigns one
      ToolCall   string    `json:"tool_call"`    // "grep(pattern=..., path=...)" one-liner
      Summary    string    `json:"summary"`      // "" until L1 runs
      Score      int       `json:"score"`        // -1 until L1 runs; 0..10 replaceability
      ResultRef  string    `json:"result_ref"`   // relative path refs/<id>.md
      ToolCallID string    `json:"tool_call_id"`
      SizeChars  int       `json:"size_chars"`
      Status     string    `json:"status"`       // "stored" | "summarized" | "compacted" | "deleted"
  }
  ```
- **Capture rule:** offload when `len(result) >= OffloadMinChars` (default **2000**). Below that, the result stays inline-only and never enters the pipeline. `finish_task`-family terminal tools and `ask_human` are never offloaded.
- Updates to entries (summary, node_id, status) are done by rewriting the JSONL under a mutex — files are small (hundreds of lines max per run). `node_id` format: `N{seq}` (we don't need TencentDB's multi-file `001-` prefix; one canvas per run).
- **`maxToolOutputChars` (`agent.go:44-47`) is removed entirely, not worked around.** Today it truncates any tool result to 60000 chars *before* it's even appended to canonical `history` (`agent.go:352-355`), so the model — and later Hindsight — permanently lose anything past that cap; no offload/recall path can recover data that was never kept. With the offload store + Compactor (§2.4) now doing token-budget-aware management of what actually reaches the LLM, a blunt fixed-size cut at capture time is redundant and strictly lossy: it exists to protect the *live context*, and that job now belongs to the Compactor. Canonical `history` keeps every tool result at full size, unconditionally.

  Practical consequence to handle explicitly, since the old cap was also incidentally guarding against a single oversized *fresh* result blowing the very next request (the Compactor's Mild tier, per §2.4, only touches tool results older than `freshAssistantTurns`): the freshness exemption becomes **size-aware, not just turn-count-aware.** If a just-produced tool result alone exceeds `shortterm_single_result_max_ratio` (default **0.15**) of the context window, it is immediately given the compacted/pointer treatment in that same `Compact` call regardless of recency — the Compactor, not a capture-time cap, is what prevents a single huge dump from starving the rest of the conversation.

### 2.2 L1 summarizer (`pkg/shortterm/summarizer.go`)

Turns raw entries into `{summary, score}` using the **utility model** (`Settings.UtilityModel`/`UtilityProviderID`, same channel Hindsight and `ask_artifact` use — `native_engine.go:146`), via a plain `aicli.Client`.

- **Trigger:** a background goroutine per run, kicked whenever pending (un-summarized) entries reach **L1BatchSize = 4**, or immediately when the Compactor needs scores it doesn't have (blocking flush, bounded to one batch).
- **One call per batch.** Prompt (system):
  > You condense agent tool-call results. For each numbered tool call below, return strict JSON `[{"i": n, "summary": "...", "score": s}]`. `summary`: one factual sentence (≤120 chars) with concrete values (counts, paths, key findings) — it must let an agent decide whether it needs the full output. `score`: 0–10 replaceability — 10 = pure noise, safe to drop (progress logs, retries, boilerplate); 5 = routine intermediate data; 0 = irreplaceable (task-critical facts, user-provided data, error messages that explain a failure).

  User content: for each pair, the tool one-liner + result head/tail (first 1500 + last 500 chars).
- **Degraded mode:** on error/timeout (10s), set `Summary = "[unsummarized] " + head(result, 200)`, `Score = 5`. Never block the agent loop on L1.
- Temperature 0.2, `reasoning_effort` low if supported.

### 2.3 L2 canvas builder (`pkg/shortterm/canvas.go`)

Maintains one Mermaid file per run: `{runLogDir}/offload/task.mmd`.

- **Format** (kept compatible with TencentDB's so their conventions/docs transfer):
  ```
  %%{ "taskGoal": "Implement CSV export for reports", "createdTime": "...", "updatedTime": "..." }%%
  graph TD
      N1["read reports controller<br/>status: done"]
      N2["located export gap in api.go<br/>status: done"]
      N3["writing csv_export.go<br/>status: doing"]
      N4["add endpoint test<br/>status: todo"]
      N1 --> N2 --> N3 --> N4
  ```
  Node text = compressed L1 summary (≤100 chars, no quotes/brackets — sanitize for Mermaid). Statuses: `done | doing | todo`.
- **Trigger:** background, when ≥ **L2NullThreshold = 4** summarized entries have `NodeID == ""`, or **L2TimeoutSeconds = 300** elapsed since last build with any pending entry. Skipped entirely until the first Mild compaction has occurred *or* pending entries exist — cheap runs never pay for a canvas.
- **One utility-model call.** Input: current `.mmd` content, the latest real user message (cached by the agent loop), and the pending entries `{tool_call_id, tool_call, summary}`. Output contract (strict JSON): `{"mmd": "<full new file content>", "mapping": {"<tool_call_id>": "<node_id>"}}`. Prompt instructs: merge new steps into the existing graph (append or restructure), keep node count ≤ **CanvasMaxNodes = 30** by collapsing old `done` chains into single summary nodes, mark at most 1–2 nodes `doing`, set `taskGoal` from the user message if empty.
- **Backfill:** apply `mapping` to the index. Unmapped entries get the most frequent mapped node_id as fallback (TencentDB's rule); still-unmapped stay pending for the next round.
- **Validation:** reject the LLM output if it doesn't parse as a Mermaid graph header + `%%{...}%%` metadata; keep the previous file. Node ids must match `^N\d+$`.

### 2.4 Compactor (`engine/aicli/compact.go`, replacing `pruneHistory`)

The core change in the agent loop. Today `runMessageHistory` sends `pruneHistory(history)` (`agent.go:207`); it will send `a.compactor.Compact(history)` — same contract: **pure function of history, never mutates the canonical in-memory slice**, returns the outgoing payload.

- **Token accounting:** reuse `tokens.Estimate` (`pkg/tokens/tokens.go`, chars/4) summed over messages + a fixed overhead per message. New per-run value `contextWindow` (settings, default **200000**; see §3). Thresholds:
  - `mild = MildOffloadRatio * window` (default **0.5**)
  - `aggressive = AggressiveCompressRatio * window` (default **0.85**)
  - `emergency = EmergencyCompressRatio * window` (default **0.95**), target `EmergencyTargetRatio` (**0.6**)
- **Fast path:** if the estimate is < 85% of `mild`, return history with only today's existing behavior applied (stale-MCP `[omitted]` replacement stays as-is) — zero new cost on small runs.
- **Tier 1 — Mild** (`>= mild`): walk tool messages older than `freshAssistantTurns = 2` (keep the existing recency guarantee) **plus any fresh result that alone exceeds `shortterm_single_result_max_ratio` of the window** (the replacement for the removed `maxToolOutputChars` cap — see §2.1), oldest-and-largest first, **highest score first** among the rest; replace each offloaded one's content with:
  ```
  [offloaded → node {NodeID or ToolCallID}] {L1 summary}
  Full result: recall_tool_result("{ToolCallID}")
  ```
  Mark `Status = "compacted"` in the index. Stop when under `mild`. Non-offloaded stale results keep today's 1500-char truncation. This *replaces* the crude `content[:1500]` for anything the store knows about.
- **Tier 2 — Aggressive** (`>= aggressive`): additionally **drop whole assistant(tool_calls)+tool message groups**, oldest and highest-score first, never dropping: the system message, any user message, the last 2 assistant turns, or a group containing a score ≤ 2 entry. The canvas injection (§2.5) carries the narrative of what was dropped. Mark `Status = "deleted"`.
- **Tier 3 — Emergency** (`>= emergency`, or aggressive made no progress): drop score-blind from the oldest end down to the target ratio, keeping a floor of `EmergencyMinMessages = 6` messages plus system + latest user message. This is the correctness backstop for pathological runs.
- **Tool-pair integrity invariant:** any drop/replace operates on complete `assistant(tool_calls) → tool*` groups; an assistant message with tool_calls is never separated from its tool results (OpenAI-compat providers hard-error otherwise). This is the one invariant every unit test must hammer.

### 2.5 Canvas injection + retrieval tool

- **Injection** (inside `Compact`, when a canvas file exists and tier ≥ Mild has ever fired): insert exactly one synthetic user-role message:
  ```
  <current_task_context>
  This is the live progress map of your current task. Nodes marked "doing" are the
  recent focus. Completed nodes were removed from the conversation to save space —
  do not redo them. To read any step's full tool output, call
  recall_tool_result with its node id or tool call id.

  Task goal: {taskGoal}

  ```mermaid
  {canvas}
  ```
  </current_task_context>
  ```
  Position: immediately after the latest real user message; adjusted forward so it never lands between an assistant tool_call and its results. Identified by a `Meta` marker on the synthetic `Message` (add an unexported field or a sentinel prefix) so re-injection first strips the old copy. Fingerprint (len + first 64 chars of the canvas) short-circuits no-op re-injections. Budget: skip injection if canvas tokens > `MmdMaxTokenRatio` (**0.1**, tighter than TencentDB's 0.2 since our canvas is single-file) × window.
- **Retrieval tool** — new `engine/aicli/tools/recall_tool_result.go`, standard one-file pattern (template: `tools/memory_recall.go`), registered in `native_engine.go` alongside the others and added to agent configs' `AllowedTools`:
  - `recall_tool_result(id string, query string?)` — `id` is a `node_id` or `tool_call_id`. Resolves via the index, reads the ref file, returns content capped at `RecallResultMaxChars` (a package constant scoped to this tool's own reply, default **60000** — unrelated to history storage, which is now uncapped; if `query` is given, return grep-style matching lines ±3 context instead of the head). Result of this tool is itself offload-eligible — the store handles that naturally.

### 2.6 Feeding the full history to Hindsight

**Requirement:** Hindsight must retain the complete run transcript — every user/assistant/tool message, full tool outputs — not the compacted view the LLM saw, and not just the outcome summary `RetainRunOutcome` builds today from `Task`/`Run` fields.

This is why the Compactor's contract in §2.4 matters beyond the live loop: `Compact` is a **pure, non-mutating** view over `history` built fresh per LLM call. The canonical `history` slice inside `runMessageHistory` (`agent.go:194-310`) always keeps the full, uncompacted conversation — compaction never touches it. And now that `maxToolOutputChars` is removed entirely (§2.1), canonical `history` is genuinely lossless: every tool result is kept at full size, not just "full up to 60000 chars." So the raw material Hindsight needs already exists in memory, complete, for the lifetime of the run — today it's simply discarded when the run ends, because `Agent.Run` (`agent.go:159`) only returns the final answer string. No ref-file recovery step is needed to reconstruct anything; `history` itself is already the whole conversation.

**Gap to close — surfacing the canonical history:**
- `Agent.Run` / `runMessageHistory` return only `(string, error)` today. Add a way to retrieve the full history the run produced: either change the return type to `(string, []Message, error)`, or add a callback/field (`Agent.OnComplete func([]Message)` or `Agent.LastHistory() []Message` read after `Run` returns). Prefer the return-value change — it's the smaller diff and matches Go convention; the two other `aicli.Client.Complete` call sites in `native_engine.go` (commit messages, `ask_artifact`) don't use `Agent` and are unaffected.
- `native_engine.go`'s run-completion path (the block at `native_engine.go:950-971`, right where `RetainRunOutcome` is already called) captures this history alongside the existing outcome build.

**Shape of what's sent — `pkg/hindsight/service.go`:**
- Add `RetainRunConversation(ctx, company, task, run db.Run, history []aicli.Message) error` alongside the existing `RetainRunOutcome` (keep both — the outcome item stays as the cheap, high-signal headline your mental-model synthesis already reads; the conversation items are the raw backing material).
- Chunk `history` into one `MemoryItem` per turn-group (a user or assistant message plus any tool messages it produced), not one giant blob — mirrors how `RetainRunOutcome` already scopes tags:
  - `DocumentID: fmt.Sprintf("run-%d-turn-%03d", run.ID, n)` (stable, so a retry/replay updates via `UpdateMode: "replace"` instead of duplicating).
  - `Tags`/`ObservationScopes`: same `agentTag`, `session:<rootRunID>`, `task:<refKey>`, `project:<id>` scoping `RetainRunOutcome` already computes — factor that tag-building into a shared helper both methods call.
  - `Metadata: {"run_id", "turn": n, "kind": "conversation_turn", "role": ...}` so recall/consolidation can distinguish these from the outcome doc (`kind: "outcome"`) and from project docs.
  - Size-bound each item (split further if a single turn's tool output exceeds a configurable chunk size; see §3) so ingestion/embedding cost per item stays predictable — now more important than before, since a full tool result can be arbitrarily large with no capture-time cap.
- Call both `RetainRunOutcome` and `RetainRunConversation` from the same async goroutine in `native_engine.go:963-969`, in one `Retain` batch call where practical (the `Client.Retain` signature already takes `[]MemoryItem`) to avoid a second round trip.
- Skip trivial turns to control volume: heartbeat tool calls, and tool results already fully captured by an earlier identical call in the same run (hash-dedupe by `tool_call` + first N chars of result), are not sent as separate conversation items — this mirrors TencentDB's "skip heartbeat" rule and keeps ingestion cost proportional to actual new information, not raw turn count.

**Why this doesn't reopen the long-term-memory boundary:** Hindsight still owns every decision about consolidation, mental-model synthesis, recall ranking, and retention policy (what to keep, how long, how to summarize further). This section only changes the *input*: instead of Hindsight synthesizing its playbooks and project state from a one-paragraph outcome, it synthesizes them from the actual conversation — a strictly richer signal for the same downstream logic. No new responsibility crosses into `pkg/shortterm/`.

## 3. Configuration

Extend the settings YAML (add to **both** structs — `server/controllers/settings.go:13` and `engine/system_prompt.go:28`), all under a `shortterm` prefix, following the existing `MemoryRecallMaxTokens` precedent:

| Setting | Default | Meaning |
|---|---|---|
| `shortterm_enabled` | `false` | Master switch (parity with TencentDB's `offload.enabled: false`) |
| `shortterm_context_window` | `200000` | Assumed model context window (tokens) |
| `shortterm_offload_min_chars` | `2000` | Result size that triggers capture |
| `shortterm_mild_ratio` | `0.5` | Mild tier threshold |
| `shortterm_aggressive_ratio` | `0.85` | Aggressive tier threshold |
| `shortterm_emergency_ratio` / `_target` | `0.95` / `0.6` | Emergency trigger / target |
| `shortterm_mmd_max_token_ratio` | `0.1` | Canvas injection budget |
| `shortterm_single_result_max_ratio` | `0.15` | A single fresh tool result larger than this fraction of the window is compacted immediately, overriding the `freshAssistantTurns` exemption (replaces the deleted `maxToolOutputChars` cap) |
| `hindsight_retain_full_history` | `true` | Feed the full run transcript to Hindsight, not just the outcome summary (§2.6) |
| `hindsight_transcript_chunk_chars` | `8000` | Max size of a single conversation `MemoryItem` before it's split further |

L1/L2 batch sizes and timeouts stay as package constants until proven to need tuning. When `shortterm_enabled=false`, `Compact` degrades to exactly today's `pruneHistory` behavior — the flag gates everything, enabling safe rollout. `hindsight_retain_full_history` is independent of `shortterm_enabled`: full-history retention only needs the canonical-history-surfacing change (§2.6) and, for full fidelity on truncated results, the offload store's ref files — so it can ship in Phase 1 (see §5) even before compaction tiers are enabled.

The Summarizer/CanvasBuilder use the utility model; if none is configured, L1/L2 silently disable and only the deterministic parts run (offload capture, mild summary-less truncation with recall pointers, retrieval tool) — still a strict improvement over head-truncation.

## 4. Observability & persistence

- Each compaction pass appends a run log entry (`appendRunLog`, `agent.go:422` pattern) of type `"compaction"`: `{tier, tokens_before, tokens_after, replaced, deleted, canvas_tokens}`. Surfaces in the existing run-log UI for free.
- `db.RunTokenStats` (`db/models.go:180`) gains `OffloadSavedTokens int64` (AutoMigrate handles it; no production data exists per `docs/memory-layer-design-review.md`).
- The offload dir lives inside the run log dir, so existing log retention/cleanup covers GC — no reclaimer needed (TencentDB's `reclaimer.ts` exists because their store is global under `~/.openclaw`; ours is per-run).
- This closes the design review's open Finding #2 ("retain full session conversations, not just outcomes") — §2.6 wires ref files + canonical history directly into `RetainRunConversation`, so it's no longer a future follow-on.

## 5. Implementation phases

**Phase 1 — Deterministic core (no LLM).** Delete `maxToolOutputChars` and its truncation call site (`agent.go:44-47, 352-355`) so canonical `history` is unconditionally lossless; `pkg/shortterm/store.go`; capture in `executeToolCalls`; `recall_tool_result` tool; `Compactor` with Mild-as-truncation (recall pointer, no summaries, including the size-aware freshness override) + Emergency tier; settings plumbing; unit tests (extend `engine/aicli/prune_test.go` style: pair-integrity, threshold math, fresh-turn preservation, oversized-fresh-result compaction). *Ship value: full results recoverable, pointer-based truncation, nothing silently dropped at capture time.*

**Phase 1.5 — Full-history retention to Hindsight (§2.6).** Surface canonical `history` from `Agent.Run`, add `RetainRunConversation` to `pkg/hindsight/service.go`, wire ref-file recovery for truncated results, chunk-and-tag conversation `MemoryItem`s, call alongside `RetainRunOutcome` in `native_engine.go`. Depends only on the offload store (Phase 1), not on the Compactor tiers or L1/L2 — can ship and be verified independently (compare Hindsight bank contents before/after on a test run). *Ship value: Hindsight's synthesis is no longer starved to one paragraph per run.*

**Phase 2 — L1 summaries + scores.** Summarizer, degraded mode, score-ordered Mild, Aggressive tier. Test with a stub `aicli.Client` (httptest server, as in existing client tests).

**Phase 3 — L2 canvas + injection.** CanvasBuilder, backfill, fingerprinted injection, `<current_task_context>` prompt. Golden-file tests for Mermaid parsing/validation.

**Phase 4 — Tuning + telemetry.** Compaction run-log entries, `OffloadSavedTokens`, frontend badge on runs (optional), threshold tuning against real runs, then consider flipping `shortterm_enabled` default. Also tune `hindsight_transcript_chunk_chars` and dedupe aggressiveness against observed Hindsight ingestion cost/latency.

## 6. Explicitly out of scope

- TencentDB's L0–L3 layered long-term memory, personas, scenarios, sqlite-vec, Hermes gateway, backend L1/L2 services — long-term memory remains Hindsight's job.
- Hindsight's own consolidation/recall/retention-policy logic — §2.6 changes what Hindsight is fed, not how it decides what to keep or how mental models are synthesized from it.
- Cross-run canvas reuse (their multi-`.mmd` session registry). One run = one canvas.
- Exact tokenization (tiktoken). `tokens.Estimate` + conservative ratios is sufficient; thresholds are ratios precisely so estimator error is absorbed.
- The LLM gateway path (`integration/llm_gateway.go`) — only the native engine loop is instrumented.

## 7. Risks

| Risk | Mitigation |
|---|---|
| L1/L2 calls add latency/cost | Async batching off the hot path; blocking flush only when the Compactor is already past Mild; utility model is the cheap tier by design |
| Bad canvas misleads the agent | Canvas is advisory ("may lag behind"), validated before write, capped at 10% of window; ground truth always one `recall_tool_result` away |
| Dropping a result the agent still needed | Score floor (≤2 never aggressively dropped), fresh-turn guarantee, and full recovery via the retrieval tool |
| Provider rejects malformed history | Pair-integrity invariant enforced in one place (`Compact`) and unit-tested exhaustively |
| Estimator undercounts → context overflow anyway | Emergency tier at 0.95 with a hard target; ratios tunable per deployment |
| Full-history retention (§2.6) multiplies Hindsight ingestion volume/cost | Chunk-size cap, heartbeat/dedupe filtering before send, `hindsight_retain_full_history` kill switch, batched into the existing async `Retain` call so it never blocks run completion |
| Removing `maxToolOutputChars` lets a single pathological tool result (e.g. a multi-hundred-MB file dump) balloon in-memory `history` and per-run disk usage | Compactor's size-aware freshness override (`shortterm_single_result_max_ratio`) compacts it out of the *next* request immediately rather than waiting on turn-count staleness; tool implementations remain the first line of defense (paginate/cap at the source) — this spec only removes the redundant second cap, not the need for tools themselves to behave |
