# headcount1

**Hire an AI company.** headcount1 is an open-source agent orchestrator. It staffs your project with a CEO, a CTO, coders, QA, designers and marketers that take tasks from a board, plan and delegate the work, build it in a sandbox, verify it, and come back to you only when a decision is yours.

[Website](https://headcount1.ai) · [Cloud version](https://app.headcount1.ai) · [Follow @neverknower_dev on X](https://x.com/neverknower_dev)

It ships as a single Go binary with the React web UI embedded, and runs on SQLite out of the box or on PostgreSQL.

## What it does

- **A company of agents.** 13 built-in roles (CEO, CTO, CMO, Coder, QA Lead, QA Manual, QA, Debugger, UX Designer, Graphic Designer, SMM, Writer, Ads Manager), plus your own custom agents and skills.
- **A task board.** Companies, projects, sprints and tasks, with a hierarchical task view, task relations and live updates over WebSocket.
- **Any model.** Connect your own LLM providers, pick a model per agent, or use model groups with fallbacks.
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
| `engine/` | Agent runtime, orchestrator, tool policy, built-in agent configs |
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
