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

This creates an executable file named `orchestrator`.

### 2. Running the Server
You can run the generated binary directly. By default, it will create a local SQLite database named `orchestrator.db` and perform automatic migrations on startup!

```sh
./orchestrator
```

**PostgreSQL Support (Optional)**:
If you prefer to use an external PostgreSQL database, you can supply a Postgres connection string via the `DATABASE_URL` environment variable:
```sh
export DATABASE_URL="postgres://username:password@localhost:5432/orchestrator?sslmode=disable"
./orchestrator
```

The server will start on port `8080`. You can access the UI at [http://localhost:8080](http://localhost:8080).

## Secrets Encryption at Rest

User-supplied credentials (LLM provider API keys, MCP auth tokens) are never stored raw. Each secret is encrypted with AES-256-GCM under a random **data key**; the data key is itself stored wrapped (encrypted) by a **master key** in `~/.headcount1/keystore.json` (envelope encryption). Secrets are decrypted in memory only at the moment they are used for an outbound request, and the API never returns them to the browser — clients only see a `has_api_key` / `has_token` flag.

The master key is taken from the first configured source:

1. **HashiCorp Vault** — set `VAULT_ADDR` and `VAULT_TOKEN`. The key is read from the KV secret at `HEADCOUNT1_VAULT_SECRET_PATH` (default `secret/data/headcount1`, KV v2), field `HEADCOUNT1_VAULT_SECRET_FIELD` (default `master_key`). The fetched key is cached in memory for `HEADCOUNT1_VAULT_KEY_TTL_SECONDS` (default 300), so revoking the Vault token locks the app out of all stored secrets within one TTL. If Vault is configured but unreachable, secret operations fail loudly — there is no silent fallback to a weaker source.

   ```sh
   # one-time setup
   vault kv put secret/headcount1 master_key="$(openssl rand -hex 32)"
   # run
   export VAULT_ADDR=https://vault.example.com:8200
   export VAULT_TOKEN=...
   ./orchestrator
   ```

2. **Environment variable** — set `HEADCOUNT1_MASTER_KEY` (64 hex chars, base64 of 32 bytes, or any passphrase, which is SHA-256-derived).

3. **Key file (zero-config default)** — with neither of the above set, a random key is auto-generated at `~/.headcount1/master.key` (mode 0600). This protects database dumps, the filesystem mirror, and backups, but not an attacker with full filesystem access as the same user — use Vault or the env var for stronger isolation.

Existing installs upgrade automatically: any secrets stored in plaintext by older versions are encrypted on the next startup. Backups include the keystore (safe — it holds only the wrapped data key) but never the master key itself; to restore a backup on a new machine, configure the same master key source first.
