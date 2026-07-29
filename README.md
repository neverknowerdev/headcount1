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
export DATABASE_URL="postgres://username:password@localhost:5432/orchestrator?sslmode=disable"
./orchestrator
```

The server will start on port `8080`. You can access the UI at [http://localhost:8080](http://localhost:8080).

## Accounts & Multi-User

Authentication is **passwordless** — every account is a **WebAuthn passkey**. Users self-register at `/register` (Face ID / Touch ID / a security key; no passwords are ever stored), and everything — companies, projects, tasks, agents, LLM providers, MCP credentials, model groups — belongs to the user who created it. WebSocket events are delivered only to the owning user's clients.

- **Sessions** are httpOnly cookies backed by a short-lived **access token** (1-hour sliding window) plus a rotating **refresh token**. A refresh-token family has a hard absolute cap (14 days by default, `SESSION_ABSOLUTE_CAP` in days); the UI proactively prompts to re-authenticate before that ceiling (`SESSION_REAUTH_GAP`). Logout revokes the family immediately; refresh-token reuse trips family-wide revocation.
- **Recovery** (`/recover`) emails a reset link. Confirming it **crypto-shreds the user's secrets** — API keys, MCP tokens, and SSH keys become unrecoverable — and lets the user re-enroll a fresh passkey. The account, teams, companies, and tasks are all preserved; only the encrypted credentials are lost (there is no master key that could recover them — that's the point). Configure `SMTP_HOST`, `SMTP_PORT` (587 STARTTLS default, 465 implicit TLS), `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, and `APP_BASE_URL` for the email; without SMTP the link is printed to the server log.
- **Teams**: an owner can invite teammates (`APP_BASE_URL` builds the invite link). Members share the owner's companies but are restricted from destructive actions (creating/deleting companies or projects, deleting MCP servers).
- **Deploying on a real domain** requires pointing the WebAuthn relying-party config at your host — see [`doc/domain-deployment.md`](doc/domain-deployment.md).

## Secrets Encryption at Rest — Zero-Knowledge

User-supplied credentials (LLM provider API keys, MCP auth tokens, SSH keys) are **never stored raw** — not in the database, not in the filesystem mirror, not in backups. Each secret is AES-256-GCM-sealed under its owning user's **data-encryption key (DEK)** and stored self-describingly as `enc:u1:<userID>:<base64>`.

The design is deliberately **zero-knowledge**: a user's DEK exists only in an **in-memory keyring**, unwrapped at login by their passkey's WebAuthn **PRF** output and evicted on logout. There is **no server-held master key** — nothing on the box (no `master.key`, no `keystore.json`, no KMS-wrapped root key) can decrypt a user's secrets while that user is signed out. Compromising the server at rest yields only ciphertext.

- Secrets are decrypted in memory only at the exact moment they're used for an outbound request; a locked (signed-out) user's secret returns a clear "vault locked — re-authenticate" error rather than a decrypt failure. The API never returns secret values to the browser — clients see only a `has_api_key` / `has_token` flag.
- Deleting a user (or account recovery) crypto-shreds every secret they own.

### Seamless restarts (boot key)

Because DEKs live only in memory, a plain restart would force every active user to re-tap their passkey. An optional **boot key** seals the in-memory keyring on a graceful shutdown and restores it on the next boot, avoiding the re-tap — it protects only that transient restart snapshot and never decrypts secrets at rest. It's off by default (safe); `make run-dev` and `scripts/run.sh` enable a zero-config local boot key. See [`doc/boot-key.md`](doc/boot-key.md).

### Environments — secrets agents can use but never see

Each company has **environments** (built-in: `headcount1 cloud` — the default, `preview`, `production`, plus any user-defined ones), each holding named secrets (`API_KEY=…`). A task runs in one environment (set per task, defaulting to `headcount1 cloud`), and its agent's shell receives that environment's secrets as **env vars**: the agent writes `curl -H "Authorization: Bearer $API_KEY"` and it just works. But the agent can never *see* a value — expansion happens inside the child process only, and every echo (`echo $API_KEY`, `env`, error text) is redacted before it reaches the LLM or any log. Values are sealed with the owner's passkey-unlocked key like all other secrets. The `headcount1 cloud` environment additionally lists the platform-managed credentials (provider API keys, MCP tokens, git SSH key) as a separate read-only group — those are used server-side by reference and are never injected into the agent's shell.

### Secrets never enter LLM message history

Decrypted secrets are kept out of the conversation sent to LLM providers and out of every persisted copy of it. Each secret the server decrypts (and each gateway run token it mints) is registered with a redaction registry (`pkg/secrets/redact`); tool outputs are scrubbed **before** they enter the agent's message history, and every run-log sink — the JSONL trajectory files, the `runs.log_entries` column, and the live WebSocket stream — scrubs again on write. Registered values are caught in raw, base64, URL-escaped, and JSON-escaped forms, and high-precision patterns additionally redact secrets the server never saw (PEM private keys, well-known API-token shapes, `Authorization` headers, passwords in connection URLs, `.env`-style assignments an agent might read from a workspace).

### Hardening the agent sandbox

The agent's shell tool runs as the server's user by default and can read the server's at-rest files. For shared/multi-tenant hosts, run the agent under a dedicated uid and/or hide the data directory from it — see [`doc/sandbox-hardening.md`](doc/sandbox-hardening.md).
