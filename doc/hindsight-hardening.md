# Hardening the memory backend (hindsight-api)

Why this exists: `hindsight-api` is a full read/write API over **every team's**
memory, and it listens on loopback. The app's own `/api/memory/*` routes enforce
team membership, but an agent that talks to the backend port **directly** skips
those checks entirely. Agents have several ways to reach loopback:

| Path | Sandboxed? | Notes |
|---|---|---|
| `web_fetch` tool | **No** — runs in the server process | takes an arbitrary URL |
| `browser_use` tool | **No** — chromedp in-process | can navigate to loopback |
| `exec_command` shell | Landlock, but `RestrictPaths` only | no socket rules; `curl` works |

So the backend must not trust the network it sits on.

---

## 1. API key authentication (implemented)

hindsight-api ships a built-in credential check,
`ApiKeyTenantExtension`. Its default extension (`DefaultTenantExtension`)
authenticates *nobody*, so this must be turned on explicitly.

`Manager` does this automatically:

- `resolveAPIKey()` mints a random 32-byte key per process. It is **never
  persisted** — not in settings, not in the DB. If `HINDSIGHT_API_TENANT_API_KEY`
  is already set in the environment (external/shared backend, e2e mock) that
  value is used instead.
- `buildEnv()` passes to the child:
  ```
  HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension
  HINDSIGHT_API_TENANT_API_KEY=<key>
  ```
- `Client` sends `Authorization: Bearer <key>` on every request.

Verified against hindsight-api 0.6.1: without the header `/v1/default/banks`
returns `401 {"detail":"Authentication failed: Invalid API key"}`; with it, `200`.
`/health` stays unauthenticated, which is what `Manager.adopt` polls.

The e2e mock enforces the same rule (401 without a valid bearer) so a regression
that drops the header fails the suite instead of silently leaving the real
backend open.

### Residual risk this does *not* close

The key reaches the child through its **environment**. Under a shared uid a
sandboxed shell can read `/proc/<hindsight_pid>/environ` and recover it —
`engine/aicli/tools/sandbox_exec_linux.go` already documents this:

> `/proc` is granted because toolchains read `/proc/self`, `/proc/cpuinfo`, etc.,
> but Landlock cannot exclude just `/proc/<other-pid>`. Under a SHARED uid …
> `/proc/<server_pid>/environ`; **only a dedicated uid closes that**.

`web_fetch` and `browser_use` cannot read files, so the key already stops those
two paths outright. Closing the shell path needs section 2.

---

## 2. Running hindsight-api as a dedicated OS user

Two independent reasons:

1. **Secret containment** — a different uid makes `/proc/<pid>/environ`
   unreadable from the agent shell, closing the leak above.
2. **It is required anyway on bare metal.** hindsight-api's embedded PostgreSQL
   (pg0) refuses to run as root:
   ```
   initdb: error: cannot be run as root
   initdb: hint: Please log in (using, e.g., "su") as the (unprivileged) user
           that will own the server process.
   ```
   Any deployment where the app runs as root must already solve this.

### Manual setup

```bash
# 1. Create a service account with no login shell and no password.
sudo useradd --system --create-home --home-dir /var/lib/headcount1-memory \
             --shell /usr/sbin/nologin headcount1-memory

# 2. Give it the venv (read+execute) and its own state directory.
#    $APP_HOME is the app's base path (e.g. ~/.headcount1).
sudo chmod -R a+rX "$APP_HOME/venv"
sudo install -d -o headcount1-memory -g headcount1-memory -m 700 \
     /var/lib/headcount1-memory

# 3. The memory export/import directory must be shared between the app (writer
#    of backups) and the backend. Use a group both belong to.
sudo groupadd -f headcount1
sudo usermod -aG headcount1 headcount1-memory
sudo install -d -o root -g headcount1 -m 2770 "$APP_HOME/hindsight"

# 4. Verify the agent cannot read the service user's environment.
sudo -u "$AGENT_UID" cat /proc/$(pgrep -f hindsight-api)/environ   # must fail
```

The child then runs with `HOME=/var/lib/headcount1-memory` so pg0 puts its
cluster (`~/.pg0`) and the model cache under that account.

### Verifying it worked

```bash
ps -o user= -p "$(pgrep -f hindsight-api)"      # -> headcount1-memory
curl -s -o /dev/null -w '%{http_code}\n' localhost:8888/v1/default/banks   # -> 401
```

---

## 3. Proposed: an app-managed service user

Doing section 2 by hand is fine for one operator and bad for everyone else. The
app already needs a non-root identity for hindsight, and the agent sandbox would
benefit from one too (a dedicated uid is the only way to fully separate the
agent from the server's memory and `/proc`). That argues for making it a
first-class app concept rather than a README step.

**Shape.**

- **Setting**: `service_user` in `appsettings.Settings` (env override
  `HEADCOUNT1_SERVICE_USER`). Empty = current behaviour (run as self).
- **New package `pkg/procuser`**, a small helper over `os/exec`:
  ```go
  // Resolve looks up the configured service account once at startup.
  func Resolve(name string) (*User, error)   // uid/gid/home, or a clear error
  // Apply makes cmd run as u. No-op when u is nil or already that uid.
  func (u *User) Apply(cmd *exec.Cmd)        // SysProcAttr.Credential + HOME=
  // EnsureOwned makes a directory writable by the service user.
  func (u *User) EnsureOwned(path string, mode os.FileMode) error
  ```
  Switching uid needs the parent to be root (or hold `CAP_SETUID`). When the app
  is **not** root, `Apply` must be a documented no-op with one warning line —
  never a hard failure, or every dev machine breaks.
- **Callers**: `hindsight.Manager.startWithSchema` (first user), and later the
  agent sandbox exec. Both already build an `exec.Cmd`, so this is a one-line
  change at each site.
- **Setup scripts** (`pkg/setup/scripts/setup-*.sh`) create the account when run
  with privileges and print the manual commands otherwise.
- **Ownership**: `filesystem.Paths.HindsightDir()` (memory exports) and the venv
  need group access; `Manager` calls `EnsureOwned` on the former at startup.

**Deliberately out of scope**: the app should never create users implicitly on a
machine it does not own. Creating the account stays an explicit setup action;
the app only *uses* a configured one and fails loudly if it is missing.

**Platform note**: `SysProcAttr.Credential` is POSIX-only. Windows needs a
different mechanism entirely, so `procuser` should build to a no-op stub there
(mirroring how `reclaim.go` / `reclaim_stub.go` already split by build tag).

---

## Related hardening, not covered here

`web_fetch` and `browser_use` accept **any** URL with no host validation — no
loopback, link-local, or private-range check, and no redirect re-check. Beyond
the memory backend that also exposes the app's own API and, on a cloud host,
the instance metadata endpoint (`169.254.169.254`). An SSRF guard on those two
tools is tracked separately and is the higher-value fix of the two.
