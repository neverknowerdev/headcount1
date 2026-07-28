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

## 3. App-managed service user (implemented)

Doing section 2 by hand is fine for one operator and bad for everyone else, so
the account is now a first-class app concept: you still *create* it yourself
(step 1 above), but the app *uses* it.

**Setting**: `service_user` in `appsettings.Settings` (`settings.yaml`), env
override `HEADCOUNT1_SERVICE_USER`. Empty — the default — means "run children as
this process", i.e. exactly the previous behaviour.

```yaml
# ~/.headcount1/settings.yaml
service_user: headcount1-memory
```

**Package `pkg/procuser`**:

```go
func Configured(fromSettings string) string        // env wins over settings
func Resolve(name string) (*User, error)           // uid/gid/home; ("" -> nil, nil)
func (u *User) Apply(cmd *exec.Cmd)                // SysProcAttr.Credential + HOME/USER/LOGNAME
func (u *User) EnsureOwned(path string, os.FileMode) error
```

Behaviour worth knowing:

- **Missing account is a hard, visible error**, not a silent fallback —
  `Resolve` fails and `Manager` logs it, because "quietly ran as root anyway"
  is the failure mode that leaves an operator believing they are isolated.
- **Not root ⇒ no-op with one warning.** Switching uid needs root (or
  `CAP_SETUID`). On a dev machine `Apply` logs once and leaves the command
  alone rather than refusing to start.
- **`Apply` composes with `setProcAttrs`** — it adds `Credential` to whatever
  `SysProcAttr` is already there (Linux `Pdeathsig`), it does not replace it.
- **`HOME` is rewritten** to the account's home so pg0's cluster (`~/.pg0`) and
  the model cache land somewhere the child can actually write.

**Wiring**: `hindsight.NewManager` resolves the account once; `startWithSchema`
calls `EnsureOwned(HindsightDir(), 0750)` (memory exports are written by the app
and read by the backend) and then `Apply(cmd)`. The agent sandbox exec is the
natural next caller — a dedicated uid is the only thing that fully separates the
agent from the server's `/proc`.

**Deliberately out of scope**: the app never creates users implicitly on a
machine it does not own. Creating the account stays an explicit setup action.

**Platform note**: `SysProcAttr.Credential` is POSIX-only, so `procuser` splits
by build tag (`procuser_unix.go` / `procuser_stub.go`, mirroring `reclaim.go` /
`reclaim_stub.go`). On the stub a *configured* service user is an error rather
than a lie about isolation; the unconfigured default works everywhere.

---

## Related hardening, not covered here

`web_fetch` and `browser_use` accept **any** URL with no host validation — no
loopback, link-local, or private-range check, and no redirect re-check. Beyond
the memory backend that also exposes the app's own API and, on a cloud host,
the instance metadata endpoint (`169.254.169.254`). An SSRF guard on those two
tools is tracked separately and is the higher-value fix of the two.
