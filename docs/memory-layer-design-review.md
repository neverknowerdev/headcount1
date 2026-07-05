# Memory Layer Design Review

A review of our Hindsight integration (`pkg/hindsight`, engine + server wiring) against the
official **hindsight-architect** skill
(<https://github.com/vectorize-io/hindsight/blob/main/skills/hindsight-architect/SKILL.md>),
which encodes Vectorize's recommended methodology: the "three architecture decisions"
(what to retain, tag schema, mental models), the conversation retain pattern, recall vs
reflect economics, and bank configuration (mission / disposition / directives).

## Where our design already matches the skill

| Skill guidance | Our implementation | Verdict |
|---|---|---|
| Tags are for **identity scoping**, deterministic, never LLM-generated | `agent:<role>`, `session:<rootRunID>`, `task:<refKey>`, `project:<id>` — all derived from DB identifiers | ✅ |
| Shared bank + tags over per-user bank silos (cross-visibility is a feature) | One `runs-<companyID>` bank shared by all agents; CMO can recall CTO's experience | ✅ |
| `document_id` upsert for content that changes | `.md` files keyed `doc:<relpath>`; changed file → re-retain same id, deleted file → document delete | ✅ |
| `context` parameter to guide extraction | Set on both doc retains ("Documentation file X of project Y") and run retains | ✅ |
| Timeless reference docs → `timestamp: "unset"`; events → real timestamps | Docs `"unset"`, run outcomes RFC3339 now | ✅ |
| **Reflect is expensive — never a routine pre-response step**; use recall for context injection | Pre-task briefing and `memory_recall` use recall only; reflect only behind the explicit "Ask memory" UI action | ✅ |
| Don't build extraction/graphs/summaries yourself | We feed raw content and let Hindsight extract | ✅ |
| `tags_match: any_strict` for strict scoping | Used when `memory_recall` is called with an `agent` filter | ✅ |

## Findings and recommendations

Ordered by expected impact.

### 1. Split banks break the entity graph — consolidate to one bank per company (HIGH)

**Finding.** We use two banks: `proj-<id>` (docs) and `runs-<companyID>` (experience).
The skill is explicit: *"Each bank has its own memories, entities, graphs, config. No
cross-bank visibility"*, and the default recommendation is a **single bank with tags**.
Our split means Hindsight can never alias-resolve or graph-link the entity "ICP backend"
mentioned in `docs/icp-backend.md` with the same entity in the CTO's run outcome —
graph-strategy recall (multi-hop entity traversal) and observation consolidation each see
only half the story. We paper over it by issuing two recall requests and concatenating
results, which loses fused ranking (each bank ranks independently; no cross-set reranking).

**Recommendation.** Move to **one bank per company** (`company-<id>`), with docs retained
under `project:<id>` + `source:docs` tags (identity/source scoping, both already in place)
and run experience under the existing agent/session/task/project tags. Recall becomes one
request with proper rank fusion; entities link across docs ↔ experience; per-project doc
management still works because deletion/upsert is by `document_id` and project scoping is
by tag. Migration is cheap while adoption is young: re-run doc sync into the new bank and
accept that historical run memories start fresh (or replay them via document-transfer
export/import). Keep separate banks only if a future requirement demands hard isolation
between projects (compliance-style), which the skill says is the *only* good reason.

### 2. We retain run outcomes, not conversations — adopt the conversation pattern (HIGH)

**Finding.** The skill's core retain pattern for agent interactions: *"Retain the full
conversation each turn with document_id = session ID... Send the FULL conversation, not
just the latest message — Hindsight needs full context for extraction."* We retain only a
short synthesized outcome (result description + error) per run. That is a decent
"task outcome" memory, but it discards most of what happened in the session: intermediate
decisions, commands tried, findings, reasons. The requirement was "feed our run logs";
today we feed run *summaries*.

**Recommendation.** Retain the session transcript per run with `document_id = run-<id>`:
either once at session end (assistant/user/tool messages rendered as text, size-capped),
or incrementally with `update_mode: "append"` — the skill/docs note that append-mode delta
retain only pays LLM extraction for new chunks, which is exactly the growing-log case.
Keep the current compact outcome item too (it makes recall hits crisp), but as a second
item within the same document/retain call rather than the only signal. Use `async: true`
(already done) and consider `HINDSIGHT_API_RETAIN_BATCH_ENABLED` for 50% provider-batch
savings since run retention is never latency-sensitive.

### 3. No mental models — the biggest unused capability (HIGH)

**Finding.** We use raw facts only. The skill dedicates a third of its content to mental
models: pre-computed, auto-refreshing syntheses that make the agent "get smarter, not just
accumulate facts", fetched as a **cheap key-value lookup** suitable for every request.
Our pre-task briefing currently relies on recall alone, so an agent starting task 47 gets
scattered fact snippets instead of a maintained understanding.

**Recommendation.** Create a small fixed set of mental models per company bank, with
`trigger: {refresh_after_consolidation: true}`, and store their IDs in our DB
(the skill stresses the app must own model-ID retrieval — tags on a model filter its
*source* memories, they don't help find the model):

| Mental model | Source tags | Source query | Injected where |
|---|---|---|---|
| Project state (one per project) | `project:<id>` | "What is the current implementation state, key decisions, and known blockers of this project?" | Pre-task briefing for tasks in that project |
| Agent playbook (one per role) | `agent:<role>` | "What approaches, mistakes and lessons has this agent accumulated?" | That agent's system prompt |
| Open blockers (global) | — | "What unresolved blockers and recurring failures exist across recent tasks?" | CEO orchestration sessions |

This turns the briefing into the skill's canonical pre-response pattern: *recall (for
task-specific facts) + mental model fetch (for synthesized understanding) — NOT reflect.*
It also directly upgrades the CTO/CMO scenario: CMO reads "project state: ICP backend
implemented as of task DEC-50" instead of hoping recall surfaces the right run memory.

### 4. Bank configuration is default — set mission, disposition, directives (MEDIUM)

**Finding.** Banks are auto-created with defaults. Mission ("first-person narrative
guiding reflect"), disposition (skepticism/literalism/empathy), and directives (hard
rules) are all unset, so our "Ask memory" reflect answers and any mental-model synthesis
run with a generic personality.

**Recommendation.** On first use of a company bank, PUT a config: mission like *"I am the
collective long-term memory of {company}'s AI agent team. I track project documentation,
implementation state, task outcomes and mistakes so agents don't repeat them."*;
disposition around `skepticism: 4` (agents' self-reported successes deserve doubt),
`literalism: 3`, `empathy: 1`; directives such as "Always state which task/run a claim
comes from" and "Never present a blocked or failed attempt as a completed implementation."
Low effort, materially better reflect/mental-model output.

### 5. Recall never sees consolidated observations (MEDIUM)

**Finding.** Hindsight's background consolidation deduplicates raw facts into
**observations**, but recall defaults to `types: [world, experience]` — we never request
`observation`, so the highest-quality, deduplicated knowledge is invisible to agents and
the pre-task briefing.

**Recommendation.** In `Service.Recall`, request
`types: ["world","experience","observation"]` with `prefer_observations: true` (raw facts
superseded by an observation get dropped and backfilled). Additionally set the bank's
`observation_scopes` to include per-`project:` and per-`agent:` tag scopes so consolidation
happens at the levels we actually query.

### 6. Recall token budgets are below the recommended floor (LOW)

**Finding.** The skill's cost-conscious tier is 5,000 tokens per recall, balanced is
10,000. We use `max_tokens: 2048` for both the briefing and `memory_recall`, with
`budget: "mid"`.

**Recommendation.** Raise defaults to ~6–8k for `memory_recall` and ~4k for the automatic
briefing (it lands in every system prompt, so stay leaner there), and make both
configurable in settings alongside the utility model. Keep `budget: mid`.

### 7. Export/import misses bank config and mental models once we adopt them (MEDIUM, becomes HIGH after #3/#4)

**Finding.** Our backup path uses the document-transfer API, which carries documents,
facts, entities and (with our `include_observations=true`) observations — but **not** bank
config, mental models, or directives. Today that's lossless because we set none of those;
after adopting recommendations #3 and #4 it silently wouldn't be.

**Recommendation.** When implementing #3/#4, extend `ExportAllToDir`/`ImportAllFromDir` to
also call `GET /v1/default/banks/{id}/export` (bank template manifest: config + mental
models + directives) and `POST .../import` on restore, storing `<bank>.json` next to
`<bank>.zip`. Alternatively adopt `hindsight-admin export-bank/import-bank`, which carries
everything in one archive.

### 8. Minor alignment notes (LOW)

- **`source:docs` tag** — the skill warns against content-classification tags; ours is a
  source/identity scope (where the memory came from), which is legitimate, but don't grow
  this into `topic:*`-style tagging: the extraction pipeline handles content semantics.
- **`memory_recall` tool description** — after #3, mention mental models so agents know
  synthesized project state arrives automatically in their briefing and the tool is for
  digging deeper.
- **Doc retain is serial and synchronous** — fine for typical repos; for large doc trees
  switch to one batched retain call (`items: [...]`) with `async: true` and poll the
  operation, which is also what the files-retain endpoint does.
- **Reruns produce one document per run** (`run-<id>`), so a task's failure history is
  preserved rather than upserted away — this is intentional and consistent with the
  skill's "task outcomes" category (distinct events), not the conversation-upsert case.

## Suggested implementation order

1. **#1 single company bank** — do first; every other change layers on the final bank layout.
2. **#5 observations in recall** + **#6 budgets** — two small `Service.Recall` changes.
3. **#2 conversation retention** — engine-side, independent of UI.
4. **#4 bank config** then **#3 mental models** (config first; models build on the bank).
5. **#7 export of config/mental models** — must land in the same release as #3/#4.
6. Update the e2e mock + memory spec alongside each step (mock needs `PUT /banks/{id}`,
   mental-model CRUD, and observation-type results for #3–#5).
