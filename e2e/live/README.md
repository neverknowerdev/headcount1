# Live e2e: real agents, real tasks

The specs under `e2e/tests/` script the model with a mock provider. The specs
here do not: each one onboards a company through the UI with a real
OpenAI-compatible provider, creates a task with **New Task**, presses
**Start**, and lets real models run the whole workflow. Each spec then checks
something the agents never saw, such as a hidden acceptance test run against
the task's worktree, so a pass means the work is actually right.

Code tasks run against a local git repository the spec seeds itself, so no
GitHub access is needed. The research spec reads a page the test serves on
127.0.0.1, so the only network a run needs is the model provider.

| Spec | Task | Checked by |
|---|---|---|
| `01_research_smoke` | read a launch note, save the answer as an artifact | values made up for this run appear in the answer |
| `02_coding_slugify` | add `slugify()` with tests | the agents' tests and a hidden test pass; work committed; review approved |
| `03_bugfix_daterange` | fix an off-by-one so `npm test` passes | fix is in `src/`, `test/` untouched, hidden edge cases pass |
| `04_human_question` | add a LICENSE, asking the owner which one | the task blocks on a question, the test answers in the UI, LICENSE is MIT |
| `05_multi_agent_todo` `@full` | build a todo CLI split into subtasks | ≥2 implementation subtasks and a review, a hidden script drives the CLI |

## Running

Put the credentials in `e2e/.env.live` (gitignored):

```sh
E2E_LIVE_BASE_URL=https://openrouter.ai/api/v1   # any OpenAI-compatible endpoint
E2E_LIVE_API_KEY=...
E2E_LIVE_SMART_MODEL=...      # refine, design, plan, verify
E2E_LIVE_CHEAP_MODEL=...      # executors and commit messages; defaults to the smart model
# E2E_LIVE_TYPESAFE_KEY=...   # optional: connects the Jev classifier slot
# E2E_LIVE_CLASSIFIER_MODEL=jev-1.13
# E2E_LIVE_TASK_TIMEOUT_MIN=20
```

Then:

```sh
make e2e-live         # specs 01–04
make e2e-live-full    # also the slow @full specs
cd e2e && npx playwright test -c playwright.live.config.ts live/02_coding_slugify.spec.ts   # one spec
```

Without credentials every spec skips. Environment variables win over the
file, which is how CI passes them in.

`E2E_LIVE_BASE_URL=mock` (with any key and `e2e-smart-model` /
`e2e-cheap-model`) points the suite at the mock provider instead. Every spec
then gets through onboarding, the forms and the waits, and fails at the
content checks, because the mock does no real work. It is a quick check of
the harness itself.

Each run attaches `run-report.json` (task, tree, usage, errors, decisions)
and `journal.txt` to the report in `playwright-report-live/`, pass or fail,
and annotates each test with its token usage.

Retries are off: a rerun is a second bill and rarely a different answer.
