# headcount1

**Hire an AI company.** headcount1 is an open-source agent orchestrator. It staffs your project with a CEO, a CTO, coders, QA, designers and marketers that take tasks from a board, plan and delegate the work, build it in a sandbox, verify it, and come back to you only when a decision is yours.

[Website](https://headcount1.ai) · [Cloud version](https://app.headcount1.ai) · [Follow @neverknower_dev on X](https://x.com/neverknower_dev)

It ships as a single Go binary with the React web UI embedded, and runs on SQLite out of the box or on PostgreSQL.

## What it does

- **A company of agents.** 13 built-in roles (CEO, CTO, CMO, Coder, QA Lead, QA Manual, QA, Debugger, UX Designer, Graphic Designer, SMM, Writer, Ads Manager), plus your own custom agents and skills.
- **A task board.** Companies, projects, sprints and tasks, with a hierarchical task view, task relations and live updates over WebSocket.
- **Any model.** Connect your own LLM providers, choose a smart model that decides and a cheap one that does the work (or a model for a single task), or use model groups with fallbacks.
- **Tools.** MCP servers, a GitHub App integration for repositories and pull requests, and a built-in browser for manual QA.
- **Sandboxed work.** Each task runs in its own git worktree. Agent shells can write only to their task's workspace (Landlock on Linux, Seatbelt on macOS) and get a scrubbed environment.
- **Passkeys and a zero-knowledge vault.** No passwords. API keys, MCP tokens and SSH keys are encrypted under a key that only the owner's passkey unlocks.
- **Teams, run logs and backups.** Invite teammates, inspect every agent run and its token usage, and export or restore your data.

## Quick start

You need **Go 1.26+** and **Node.js 20.19+** (with `npm`).

```sh
git clone https://github.com/neverknowerdev/headcount1.git
cd headcount1
make build          # installs frontend dependencies, builds the UI, compiles the binary
./agent-orchestrator
```

Open [http://localhost:8080](http://localhost:8080), register with a passkey at `/register`, add an LLM provider, create a company, and give the CEO a task.

Data lives in `~/.headcount1` (a SQLite database, `headcount1.db`, and the agent workspaces). Migrations run automatically on startup.

To use PostgreSQL instead, set `DATABASE_URL`:

```sh
export DATABASE_URL="postgres://username:password@localhost:5432/orchestrator?sslmode=disable"
./agent-orchestrator
```

## How a task runs

Smart models think and decide; cheap models do the work. A task moves through an explicit workflow, and almost every unit of work in it is itself a task.

| Step | Who | What happens |
|---|---|---|
| **Refine** | smart model | Works out what must be done, resolves open questions, writes the specification and the definition of done. |
| **Design**, **Test plan** | smart model, as CTO and QA Lead | Coding tasks only: the technical design, then the test scenarios. |
| **Plan** | smart model | Splits the work into small subtasks, each with its own check, and says which must wait for which. |
| **Execute** | cheap model | Each subtask runs as an executor session with the full tool set and reports back: done, failed, or cannot be completed, and why. Coding subtasks are reviewed, including new attempts at ones that failed; a review that asks for changes triggers a fix and a second review without involving the smart model. Work that depends on a coding subtask starts only once its review has accepted it. |
| **Adjust** | smart model | Only when something failed or the plan has to change: re-plans with the full picture, including every decision made so far. |
| **Verify** | smart model | Checks the result against the definition of done. A task that passes waits **in review** for a person. |

A task has a **type** (research, coding, review, general) that selects its workflow, and may name its own **model**; otherwise it runs on the defaults under *LLM Providers → Default Models*: **smart**, **cheap**, and optionally a **classifier** and a model for **commit messages**.

A few rules hold throughout:

- **The smart model has no chat history.** Every step is one freshly composed prompt and exactly one tool call. Its tool set is tiny: ask questions (each becomes a research subtask), ask the human, create tasks, read the decision tree or the state of execution, finish the phase. It never reads or writes files.
- **Executors never ask the human.** What they cannot find out comes back to the smart model with the reason; only the smart model decides whether a question is worth a person's time. A task waiting for an answer says so, and each question is answered on its own.
- **Every decision is recorded**, with its rationale and the alternatives, under the task that made it. Smart steps record theirs as part of their one tool call. Executors are made to checkpoint while they work, at a fixed interval of tool calls, or as soon as the classifier sees a choice made, an approach abandoned or an assumption left unverified; a checkpoint that only restates an earlier record adds nothing.
- **A task is never left stale.** Everything a task waits on (its subtasks, an executor session, a person, a locked vault, a missing model) is stored on the task and re-checked by a sweeper, not held by a goroutine. A restart, planned or not, picks every task up where it was; work that fails cancels what depended on it and sends the parent to **adjust**.
- **A model that cannot be called is not a result to plan around.** When a provider refuses, is unreachable or runs out, the call is retried once after a pause. If it still fails, the task stops what is running beneath it and tells the human what the provider said, instead of asking the smart model to work around it. It carries on from the same step once they reply.
- **Questions are not asked twice.** A question that was answered, or that an executor reported it cannot answer, is refused if the smart model puts it again in other words, and a step may send out questions only a few times before it has to decide with what it has or ask the human.
- **Errors are collected on the task.** The *Errors* tab of a task lists every failed model call and crashed session beneath it, the same error once with how often it came and where.
- **Everything is logged per task**: a journal with each smart prompt and answer in full, the subtasks it led to, each executor session, and the decisions. *Logs* on a task downloads them as one archive laid out like the task tree: a folder per task named by its key (`GL-18`, with `GL-18-1` inside it), each holding `task.jsonl`, `decisions.jsonl` and one file per executor session under the session's name. On disk the same files are kept by ID, under `logs/{company}/{top-level task id}/task-{id}/`. *Run Logs* shows the sessions the same way, under their tasks.
- **Every model call is in a usage ledger.** The *Usage* page and each task's *Usage* tab break spend down by task, workflow step, agent and model; any row opens into its calls and any call into its log.

An **agent** is a role: a name, a description and a short prompt (`You are the CTO agent.`) that you can extend with what the role should always know about your company. Models, tools and MCP servers are not per-agent settings: executors get the full tool set and every MCP server of the company.

### The classifier (optional)

[TypeSafe](https://typesafe.ai)'s Jev is a **System One model**, not a language model: it answers yes/no and which-of-these questions about a text for very little, and cannot write. The engine asks it, after each executor turn, whether something worth recording has just happened (so checkpoints come at the right moment instead of only at the fixed interval), whether a new record merely rewords an existing one, whether a session is going in circles, and which reports are irrelevant to a prompt that does not fit. Nothing depends on it: with no classifier, or with one that is failing, fixed rules apply instead.

Jev is served by TypeSafe and, beside their language models, by other providers: OpenCode Zen (`jev-1.13`, and `jev-1.13-free` at no cost) and AI Surplus (`jev-1.13.0`). Wherever a provider's models are discovered, its System One models are recognised by their ID and kept in a list of their own, so they never show up where a language model is chosen:

- The **classifier** slot under *Default Models* offers only System One models and groups of them; every other slot, and a task's own model, offers only language models.
- A **model group** holds one kind. A group of System One models routes exactly as a group of language models does (free members first, failover on errors and rate limits, the same statistics) and can be chosen only as the classifier.
- Connecting or activating a provider that serves a System One model fills the classifier slot if it is still empty, preferring a free model. Until a classifier is set, *LLM Providers* and *Settings* show a warning that says how to get one.

## Configuration

Everything is configured with environment variables. The most common ones:

| Variable | Purpose |
| --- | --- |
| `PORT` | Port to listen on. Default `8080`. |
| `DATABASE_URL` | PostgreSQL connection string. Without it, SQLite in `~/.headcount1` is used. |
| `APP_BASE_URL` | Public URL of your instance, used in recovery and invitation emails. |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM` | Outgoing email. Without SMTP, links are printed to the server log. |
| `SESSION_ABSOLUTE_CAP`, `SESSION_REAUTH_GAP` | Session lifetime limits. |
| `HEADCOUNT1_TERMS_URL`, `HEADCOUNT1_PRIVACY_URL` | Links to your own Terms of Service and Privacy Policy. When set, the sign-up page shows a required acceptance checkbox and the server refuses sign-ups without it. Unset by default. |
| `HEADCOUNT1_GITHUB_APP_*` | GitHub App credentials. See [`doc/github-app.md`](doc/github-app.md). |

More guides:

- [`doc/domain-deployment.md`](doc/domain-deployment.md): running on a real domain (WebAuthn relying-party setup).
- [`doc/github-app.md`](doc/github-app.md): the GitHub App, including one app for production and staging.
- [`doc/boot-key.md`](doc/boot-key.md): keeping users signed in across restarts.
- [`doc/sandbox-hardening.md`](doc/sandbox-hardening.md): hardening the agent sandbox on shared hosts.

## Development

```sh
make run-dev        # Go server with live reload plus the Vite dev server
make run            # build the UI, then `go run .`
scripts/run.sh      # run locally with a self-managed boot key (see --help)
```

Tests:

```sh
go test ./...                 # backend
cd frontend && npm test       # frontend unit tests (Vitest)
make e2e                      # end-to-end tests (Playwright)
```

Repository layout:

| Path | Contents |
| --- | --- |
| `main.go`, `server/` | HTTP API, WebSocket hub, authentication |
| `engine/` | Task workflow, executor sessions, tools, built-in agent roles |
| `db/` | Models, repositories, migrations |
| `pkg/` | Shared packages: secrets, git, GitHub App, mailer, backup, updater |
| `frontend/` | React web UI, embedded into the binary |
| `e2e/` | Playwright end-to-end tests |
| `landing/` | The [headcount1.ai](https://headcount1.ai) website |
| `doc/` | Deployment and security guides |

## Accounts and teams

Authentication is **passwordless** — every account is a **WebAuthn passkey**. Users self-register at `/register` (Face ID / Touch ID / a security key; no passwords are ever stored), and everything — companies, projects, tasks, agents, LLM providers, MCP credentials, model groups — belongs to the user who created it. WebSocket events are delivered only to the owning user's clients.

- **Sessions** are httpOnly cookies backed by a short-lived **access token** (1-hour sliding window) plus a rotating **refresh token**. A refresh-token family has a hard absolute cap (14 days by default, `SESSION_ABSOLUTE_CAP` in days); the UI proactively prompts to re-authenticate before that ceiling (`SESSION_REAUTH_GAP`). Logout revokes the family immediately; refresh-token reuse trips family-wide revocation.
- **Recovery** (`/recover`) emails a reset link. Confirming it **crypto-shreds the user's secrets** — API keys, MCP tokens, and SSH keys become unrecoverable — and lets the user re-enroll a fresh passkey. The account, teams, companies, and tasks are all preserved; only the encrypted credentials are lost (there is no master key that could recover them — that's the point). Configure `SMTP_HOST`, `SMTP_PORT` (587 STARTTLS default, 465 implicit TLS), `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, and `APP_BASE_URL` for the email; without SMTP the link is printed to the server log.
- **Teams**: an owner can invite teammates (`APP_BASE_URL` builds the invite link). Members share the owner's companies but are restricted from destructive actions (creating/deleting companies or projects, deleting MCP servers).
- **Deploying on a real domain** requires pointing the WebAuthn relying-party config at your host — see [`doc/domain-deployment.md`](doc/domain-deployment.md).
- **One GitHub App for production and staging** is supported — see [`doc/github-app.md`](doc/github-app.md).

## Secrets: zero-knowledge encryption at rest

User-supplied credentials (LLM provider API keys, MCP auth tokens, SSH keys) are **never stored raw** — not in the database, not in the filesystem mirror, not in backups. Each secret is AES-256-GCM-sealed under its owning user's **data-encryption key (DEK)** and stored self-describingly as `enc:u1:<userID>:<base64>`.

The design is deliberately **zero-knowledge**: a user's DEK exists only in an **in-memory keyring**, unwrapped at login by their passkey's WebAuthn **PRF** output and evicted on logout. There is **no server-held master key** — nothing on the box (no `master.key`, no `keystore.json`, no KMS-wrapped root key) can decrypt a user's secrets while that user is signed out. Compromising the server at rest yields only ciphertext.

- Secrets are decrypted in memory only at the exact moment they're used for an outbound request; a locked (signed-out) user's secret returns a clear "vault locked — re-authenticate" error rather than a decrypt failure. The API never returns secret values to the browser — clients see only a `has_api_key` / `has_token` flag.
- Deleting a user (or account recovery) crypto-shreds every secret they own.

### Seamless restarts (boot key)

Because DEKs live only in memory, a plain restart would force every active user to re-tap their passkey. An optional **boot key** seals the in-memory keyring on a graceful shutdown and restores it on the next boot, avoiding the re-tap — it protects only that transient restart snapshot and never decrypts secrets at rest. It's off by default (safe); `make run-dev` and `scripts/run.sh` enable a zero-config local boot key. See [`doc/boot-key.md`](doc/boot-key.md).

### Hardening the agent sandbox

The agent's shell tool runs as the server's user by default and can read the server's at-rest files. For shared/multi-tenant hosts, run the agent under a dedicated uid and/or hide the data directory from it — see [`doc/sandbox-hardening.md`](doc/sandbox-hardening.md).

## License

headcount1 is open source under the [GNU Affero General Public License v3.0](LICENSE). Copyright © 2026 neverknower.

- You can use, self-host and modify it for free, including in a business.
- If you distribute a modified version, or let other people use one over a network, the AGPL requires you to publish your source under the same license.
- You must keep the author attribution, "Based on headcount1 by neverknower", as described in [NOTICE](NOTICE).
- If the AGPL does not work for your organization, a commercial license is available: write to legal@headcount1.ai. The hosted version at [app.headcount1.ai](https://app.headcount1.ai) is the other option.

The headcount1 name and logo are not covered by the AGPL. See the [trademark policy](TRADEMARKS.md).

Contributions are welcome under the [Contributor License Agreement](CLA.md). See [CONTRIBUTING.md](CONTRIBUTING.md).

## Author

headcount1 is built by neverknower. Follow [@neverknower_dev on X](https://x.com/neverknower_dev) for updates, and see [headcount1.ai](https://headcount1.ai) for the product. headcount1 Cloud is operated by GMGM sp. z o.o.
