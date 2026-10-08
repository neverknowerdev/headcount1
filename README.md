# Agent Orchestrator MVP

This is an MVP implementation of an agent orchestration system. It is distributed as a single Go binary with an embedded React frontend.

## Prerequisites
- **Go**: >= 1.21
- **Node.js**: >= 18 (and `npm`)

## Local Build & Run Instructions

### 1. Building the Project
You can build the single binary containing both the frontend and backend with our provided Makefile:

```sh
# This will install frontend dependencies, build the React app, and compile the Go binary
make build
```

This creates an executable file named `agent-orchestrator`.

### 2. Running the Server
You can run the generated binary directly. By default, it will create a local SQLite database at `~/.headcount1/headcount1.db` and perform automatic migrations on startup!

```sh
./agent-orchestrator
```

**PostgreSQL Support (Optional)**:
If you prefer to use an external PostgreSQL database, you can supply a Postgres connection string via the `DATABASE_URL` environment variable:
```sh
export DATABASE_URL="postgres://username:password@localhost:5432/headcount1?sslmode=disable"
./agent-orchestrator
```

The server will start on port `8080`. You can access the UI at [http://localhost:8080](http://localhost:8080).

## How a task runs

Smart models think and decide; cheap models do the work. A task moves through an explicit workflow, and almost every unit of work in it is itself a task.

| Step | Who | What happens |
|---|---|---|
| **Refine** | smart model | Works out what must be done, resolves open questions, writes the specification and the definition of done. |
| **Design**, **Test plan** | smart model, as CTO and QA Lead | Coding tasks only: the technical design, then the test scenarios. |
| **Plan** | smart model | Splits the work into small subtasks, each with its own check, and says which must wait for which. |
| **Execute** | cheap model | Each subtask runs as an executor session with the full tool set and reports back: done, failed, or cannot be completed, and why. Coding subtasks are reviewed; a review that asks for changes triggers a fix and a second review without involving the smart model. |
| **Adjust** | smart model | Only when something failed or the plan has to change: re-plans with the full picture, including every decision made so far. |
| **Verify** | smart model | Checks the result against the definition of done. A task that passes waits **in review** for a person. |

A task has a **type** (research, coding, review, general) that selects its workflow, and may name its own **model**; otherwise it runs on the defaults under *LLM Providers → Default Models*: **smart**, **cheap**, and optionally a **classifier** and a model for **commit messages**.

A few rules hold throughout:

- **The smart model has no chat history.** Every step is one freshly composed prompt and exactly one tool call. Its tool set is tiny: ask questions (each becomes a research subtask), ask the human, create tasks, read the decision tree or the state of execution, finish the phase. It never reads or writes files.
- **Executors never ask the human.** What they cannot find out comes back to the smart model with the reason; only the smart model decides whether a question is worth a person's time. A task waiting for an answer says so, and each question is answered on its own.
- **Every decision is recorded**, with its rationale and the alternatives, under the task that made it. Smart steps record theirs as part of their one tool call. Executors are made to checkpoint while they work, at a fixed interval of tool calls, or as soon as the classifier sees a choice made, an approach abandoned or an assumption left unverified; a checkpoint that only restates an earlier record adds nothing.
- **A task is never left stale.** Everything a task waits on (its subtasks, an executor session, a person, a locked vault, a missing model) is stored on the task and re-checked by a sweeper, not held by a goroutine. A restart, planned or not, picks every task up where it was; work that fails cancels what depended on it and sends the parent to **adjust**.
- **Everything is logged per task**: a journal with each smart prompt and answer in full, the subtasks it led to, each executor session, and the decisions. The same files are on disk under `logs/{company}/{top-level task id}/task-{id}/` (`task.jsonl`, `decisions.jsonl`, and one `run-{id}.jsonl` per executor session).
- **Every model call is in a usage ledger.** The *Usage* page and each task's *Usage* tab break spend down by task, workflow step, agent and model; any row opens into its calls and any call into its log.

An **agent** is a role: a name, a description and a short prompt (`You are the CTO agent.`) that you can extend with what the role should always know about your company. Models, tools and MCP servers are not per-agent settings: executors get the full tool set and every MCP server of the company.

### The classifier (optional)

[TypeSafe](https://typesafe.ai)'s Jev is a classifier, not a language model: it answers yes/no and which-of-these questions about a text for very little. Add it under *LLM Providers* (preset "TypeSafe (Jev classifier)") and select it in the **classifier** slot. The engine then asks it, after each executor turn, whether something worth recording has just happened (so checkpoints come at the right moment instead of only at the fixed interval), whether a new record merely rewords an existing one, whether a session is going in circles, and which reports are irrelevant to a prompt that does not fit. Nothing depends on it: with no classifier, or with one that is failing, fixed rules apply instead.

## Accounts & Multi-User

Authentication is **passwordless** — every account is a **WebAuthn passkey**. Users self-register at `/register` (Face ID / Touch ID / a security key; no passwords are ever stored), and everything — companies, projects, tasks, agents, LLM providers, MCP credentials, model groups — belongs to the user who created it. WebSocket events are delivered only to the owning user's clients.

- **Sessions** are httpOnly cookies backed by a short-lived **access token** (1-hour sliding window) plus a rotating **refresh token**. A refresh-token family has a hard absolute cap (14 days by default, `SESSION_ABSOLUTE_CAP` in days); the UI proactively prompts to re-authenticate before that ceiling (`SESSION_REAUTH_GAP`). Logout revokes the family immediately; refresh-token reuse trips family-wide revocation.
- **Recovery** (`/recover`) emails a reset link. Confirming it **crypto-shreds the user's secrets** — API keys, MCP tokens, and SSH keys become unrecoverable — and lets the user re-enroll a fresh passkey. The account, teams, companies, and tasks are all preserved; only the encrypted credentials are lost (there is no master key that could recover them — that's the point). Configure `SMTP_HOST`, `SMTP_PORT` (587 STARTTLS default, 465 implicit TLS), `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, and `APP_BASE_URL` for the email; without SMTP the link is printed to the server log.
- **Teams**: an owner can invite teammates (`APP_BASE_URL` builds the invite link). Members share the owner's companies but are restricted from destructive actions (creating/deleting companies or projects, deleting MCP servers).
- **Deploying on a real domain** requires pointing the WebAuthn relying-party config at your host — see [`doc/domain-deployment.md`](doc/domain-deployment.md).
- **One GitHub App for production and staging** is supported — see [`doc/github-app.md`](doc/github-app.md).

## Secrets Encryption at Rest — Zero-Knowledge

User-supplied credentials (LLM provider API keys, MCP auth tokens, SSH keys) are **never stored raw** — not in the database, not in the filesystem mirror, not in backups. Each secret is AES-256-GCM-sealed under its owning user's **data-encryption key (DEK)** and stored self-describingly as `enc:u1:<userID>:<base64>`.

The design is deliberately **zero-knowledge**: a user's DEK exists only in an **in-memory keyring**, unwrapped at login by their passkey's WebAuthn **PRF** output and evicted on logout. There is **no server-held master key** — nothing on the box (no `master.key`, no `keystore.json`, no KMS-wrapped root key) can decrypt a user's secrets while that user is signed out. Compromising the server at rest yields only ciphertext.

- Secrets are decrypted in memory only at the exact moment they're used for an outbound request; a locked (signed-out) user's secret returns a clear "vault locked — re-authenticate" error rather than a decrypt failure. The API never returns secret values to the browser — clients see only a `has_api_key` / `has_token` flag.
- Deleting a user (or account recovery) crypto-shreds every secret they own.

### Seamless restarts (boot key)

Because DEKs live only in memory, a plain restart would force every active user to re-tap their passkey. An optional **boot key** seals the in-memory keyring on a graceful shutdown and restores it on the next boot, avoiding the re-tap — it protects only that transient restart snapshot and never decrypts secrets at rest. It's off by default (safe); `make run-dev` and `scripts/run.sh` enable a zero-config local boot key. See [`doc/boot-key.md`](doc/boot-key.md).

### Hardening the agent sandbox

The agent's shell tool runs as the server's user by default and can read the server's at-rest files. For shared/multi-tenant hosts, run the agent under a dedicated uid and/or hide the data directory from it — see [`doc/sandbox-hardening.md`](doc/sandbox-hardening.md).
