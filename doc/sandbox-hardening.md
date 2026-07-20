# Sandbox Hardening — Server Setup Guide

The agent's `bash` tool runs commands the LLM writes, so those commands are
treated as hostile. The application ships the enforcement mechanism (Landlock /
Seatbelt, env scrubbing, a re-exec that drops privileges), **but the strong
guarantees only exist once the host is set up for them.** Out of the box you get
the write sandbox and env scrub; the read/secret protection is opt-in *and*
depends on host configuration the application cannot do for itself.

This document is about that host/server setup — the missing half. For the code,
read `engine/aicli/tools/`.

---

## TL;DR — the operator checklist

| # | Task | Needed for | Who does it |
| - | ---- | ---------- | ----------- |
| 1 | Run on a kernel with **Landlock active in the LSM stack** | any real enforcement on Linux | **you (host/kernel)** |
| 2 | Allow the `landlock_*` syscalls through the container **seccomp** profile | enforcement inside containers | **you (runtime)** |
| 3 | Create a **dedicated OS user with a writable home** | `HEADCOUNT1_SANDBOX_UID` | **you (host/image)** |
| 4 | Give the server **`CAP_SETUID` + `CAP_SETGID`** | dropping to the sandbox uid | **you (runtime)** |
| 5 | Make the **workspace subtrees writable by the sandbox uid** while keeping secret files server-owned | dedicated-uid mode to actually run builds | **you — the app does NOT do this** |
| 6 | Install **toolchains under system roots**, not under `$HOME` | `HEADCOUNT1_SANDBOX_READ_SCOPING` | **you (image)** |
| 7 | Name server secrets with the **scrubbed prefixes** | env-leak protection | **you (config)** |
| 8 | Set the env vars and **verify the active mode** | turning it all on | **you** |

Steps 1–2 gate *all* enforcement. Steps 3–5 are the dedicated-uid mode. Step 6
is read-scoping. Miss step 5 and every agent build fails; miss steps 1–2 and the
sandbox silently does nothing.

---

## Default behavior (no setup)

With no configuration the agent shell gets:

- **Write sandbox** — can only write inside its workspace + scratch/cache dirs
  (`/tmp`, `~/.cache`, `~/.npm`, `~/go/pkg`, …). Enforced by Landlock (Linux) or
  Seatbelt (macOS).
- **Secret-file read denial (always on)** — the agent is denied *read* access to
  the server's own secret files, with no opt-in required: the SQLite DB
  (`${HEADCOUNT1_HOME}/db`), SSH keys (`ssh/`), `credentials/`, `backups/`, and
  the keyring snapshot / boot key (`keyring.sealed`, `keyring.bootkey`). Landlock
  is an allowlist with no deny rule, so this is done by granting read of the
  whole filesystem *minus* those subtrees (the rest of `${HEADCOUNT1_HOME}` —
  `workspace/`, `repos/`, `skills/`, `venv/`, … — stays readable); Seatbelt uses
  an explicit `(deny file-read* …)`. Reads everywhere else remain open, so
  toolchains are unaffected.
- **Env scrub** — the server's secret env vars are stripped before the shell
  runs (see step 7).

What the always-on default does **not** cover: a shared-uid agent can still read
the server's environment via `/proc/<server_pid>/environ`, and `/proc`, `/tmp`,
and other world-readable locations stay open. Closing those — and hiding the
*whole* home rather than just the known secret files — is what the
dedicated-uid (steps 3–5) and read-scoping (step 6) modes are for. On a
single-user dev box the always-on denial is usually enough; for a
shared/multi-tenant deployment, do the full setup.

`${HEADCOUNT1_HOME}` defaults to `~/.headcount1` (the server user's home).

---

## Step 1–2: Make enforcement real

The application uses `landlock.V5.BestEffort()` — meaning **if the kernel can't
enforce, the command runs unsandboxed instead of failing.** So "it didn't error"
is *not* proof the sandbox is on. You must guarantee the kernel can enforce.

**Kernel (bare metal / VM):**
- Linux **≥ 5.13** with `CONFIG_SECURITY_LANDLOCK=y` (ABI v1 = writes).
- **≥ 5.19** (ABI v2) is strongly recommended — it adds the `refer` right the
  ruleset uses for `mv`/`ln` across directories; without it those operations may
  be denied inside the workspace.
- Landlock must be in the **active LSM list**, not just compiled. Check:
  ```
  cat /sys/kernel/security/lsm      # must include "landlock"
  ```
  If it's missing, add it to the kernel command line and reboot:
  ```
  lsm=landlock,lockdown,yama,integrity,apparmor   # keep your distro's existing list, prepend landlock
  ```

**Containers (Docker / Kubernetes):**
- The **host** kernel provides Landlock (containers share it). A managed node
  without Landlock → no enforcement.
- The container's **seccomp** profile must permit `landlock_create_ruleset`,
  `landlock_add_rule`, `landlock_restrict_self`. Docker's default profile allows
  them on current versions; a custom/locked-down profile may not.
- **gVisor / Kata / emulated runtimes** may not implement Landlock at all → the
  sandbox degrades to unsandboxed. Verify (Verification section) rather than
  assume.

If you cannot get Landlock, the write sandbox is your only kernel guard and
read-scoping/uid isolation are unavailable — plan accordingly.

---

## Step 3–5: Dedicated sandbox uid

Runs the agent shell as a separate unprivileged user so OS file permissions —
underneath Landlock — stop it reading the server's secret files and the server's
`/proc/<pid>/environ`.

### 3. Create the user (with a real, writable home)

```
useradd --create-home --shell /usr/sbin/nologin hc1-sandbox
id -u hc1-sandbox      # → use this as HEADCOUNT1_SANDBOX_UID
```

The home **must exist and be owned by this user.** The application resolves
toolchain caches (`~/.cache`, `~/.npm`, `~/go/pkg`) against *this uid's* home
(via `user.LookupId`). If the user isn't a resolvable passwd entry, or its home
isn't writable, `go build` / `npm install` fail under the write sandbox. Don't
use a homeless system account.

### 4. Give the server permission to drop privileges

Switching uid needs `CAP_SETUID`/`CAP_SETGID`. Simplest: run the server as root
(it drops to the sandbox uid only for agent commands). If it lacks the
capability, **command start fails loudly** — it never silently runs the agent
with server privileges.

- **systemd:** `AmbientCapabilities=CAP_SETUID CAP_SETGID` (or run as root).
- **Kubernetes:**
  ```yaml
  securityContext:
    capabilities:
      add: ["SETUID", "SETGID"]
  # do not set runAsNonRoot: true — the server needs to perform the drop itself
  ```
- **Docker:** running as root is enough; with `--user` you must add the caps.

The sandbox process's credentials are set with `Groups=[gid]` (and
`NoSetGroups=false`), so the kernel calls `setgroups()` and **clears the
server's supplementary groups** — the child ends up in only its primary gid
(`HEADCOUNT1_SANDBOX_GID`, default = the uid). Supplementary-group tricks won't
grant it access; plan file permissions around that one gid.

### 5. Make workspaces writable by the sandbox uid — **the app does NOT do this**

This is the step people miss. The server creates its whole data tree
`0755` **owned by the server uid**. The agent now runs as `hc1-sandbox`, which
**cannot write those directories** → every file-writing command the agent runs
fails.

You must arrange the permissions yourself, splitting `${HEADCOUNT1_HOME}` into
two zones:

| Zone | Paths under `${HEADCOUNT1_HOME}` | Requirement |
| ---- | -------------------------------- | ----------- |
| **Secrets — keep away from the sandbox uid** | `keyring.sealed`, `keyring.bootkey` (self-managed boot key, if used), `db/`, `ssh/`, `credentials/`, `backups/` | owned by the **server** uid, mode `0600`/`0700`, **not** readable by the sandbox uid |
| **Workspaces — must be writable by the sandbox uid** | `workspace/`, `repos/`, `artifacts/`, `logs/`, `uploads/`, `skills/`, `venv/` | writable by the sandbox uid/gid |

Because dedicated-uid mode implies the server runs as root, the clean approach is
to **chown the workspace zone to the sandbox uid** and leave the secret zone
root-owned:

```
HOME_DIR=/root/.headcount1          # = ${HEADCOUNT1_HOME}
# workspace zone → sandbox-writable
for d in workspace repos artifacts logs uploads skills venv; do
  install -d -o hc1-sandbox -g hc1-sandbox -m 0755 "$HOME_DIR/$d"
done
# secret zone → server-only, unreadable by others
for s in db ssh credentials backups; do chmod 0700 "$HOME_DIR/$s"; done
chmod 0600 "$HOME_DIR/keyring.sealed" "$HOME_DIR/keyring.bootkey" 2>/dev/null || true
chmod 0711 "$HOME_DIR"              # traversable, but not listable/readable by the sandbox uid
```

The root server can still write into the sandbox-owned workspace dirs (root
bypasses permissions); the sandbox uid can write its workspace but is blocked by
`0600`/`0700` from the secret zone. (Alternative to chown: a shared gid equal to
`HEADCOUNT1_SANDBOX_GID` on the workspace dirs with setgid `2775` + a server
`umask 002`. Either works; the chown route is simpler when the server is root.)

> Re-run the workspace-zone ownership fixup whenever new company/project/task
> dirs are created if you are **not** running the server as root. As root, no
> fixup is needed — root writes them and the sandbox uid reads/writes via the
> ownership you set on the parents (use setgid dirs so children inherit).

---

## Step 6: Read-scoping — toolchains must live under system roots

The always-on default already denies the *known* secret files (see "Default
behavior"). Read-scoping goes further: `HEADCOUNT1_SANDBOX_READ_SCOPING=1` swaps
Landlock's broad read grant for an allowlist of system roots that **excludes the
whole home directory**, so the agent can't read *anything* under
`${HEADCOUNT1_HOME}` — not just the enumerated secrets — even at the server's own
uid.
Enable it on its own (cheap, no uid needed) or alongside the dedicated uid for
defense in depth.

> **Read-scoping is not a substitute for the dedicated uid.** Landlock filters by
> path, not by process. `/proc` must stay on the allowlist (toolchains read
> `/proc/self`, `/proc/cpuinfo`), and Landlock can't exclude just
> `/proc/<other-pid>` — so under a **shared uid** the agent can still read
> `/proc/<server_pid>/environ` (and, with a permissive `yama`, `/proc/<pid>/mem`)
> to reach the very secrets scrubbing removed from its own environment. Only a
> **dedicated uid** (a different uid → the kernel denies `/proc/<pid>` access)
> closes that. Treat the dedicated uid as mandatory when secrecy matters.

The allowed read roots are: `/usr /bin /sbin /lib* /etc /opt /proc /sys /dev
/snap`, plus `/var/tmp /var/cache /var/lib`, the workspace, the toolchain caches,
and any read-only roots passed to the tools. The wholesale `/run` and `/var`
trees are **deliberately not granted**: they hold world-readable container secret
mounts (Docker `/run/secrets/*`, Kubernetes
`/var/run/secrets/kubernetes.io/serviceaccount/token`, systemd credentials) that
would otherwise be reachable **even with a dedicated uid**. **Anything the build
must read that lives elsewhere is denied.** Practical consequences for your image:

- Install language runtimes/toolchains under **system paths** (`/usr/local`,
  `/opt`, `/usr`) — **not** under `$HOME` (`~/.asdf`, `~/.nvm`, `~/.rbenv`,
  `~/.local`, `~/go/bin`). Home-installed toolchains become unreadable and won't
  execute.
- Caches under `~/.cache`, `~/.npm`, `~/go/pkg` are granted read+write, so they
  keep working — but executables in `~/go/bin` / `~/.local/bin` are not on the
  read/exec allowlist. Put binaries in `/usr/local/bin`.
- Extra data the agent legitimately needs to read (a shared dataset, a parent
  task's workdir) is exposed via the tools' **read-only roots**, not this list —
  those are granted explicitly and remain readable.

---

## Step 7: Environment scrubbing — name your secrets correctly

Before running the shell the server removes secret env vars by
**prefix/name/substring** (best effort). Removed: anything starting
`HEADCOUNT1_`, `VAULT_`, `AWS_`, `AZURE_`, `GCP_`, `GOOGLE_`, `SMTP_`, `SSH_`;
the exact names `DATABASE_URL`, `REDIS_URL`; and any name containing `TOKEN`,
`SECRET`, `PASSWORD`, `PASSWD`, `PASSPHRASE`, `CREDENTIAL`, `KEY`, `DSN`, or
`URI`. So `MYCORP_API_TOKEN`, `SIGNING_KEY`, and `SENTRY_DSN` are all caught.

This is a **denylist**, so it is best-effort: an unconventionally named secret
(e.g. a bare `LICENSE` value that is actually sensitive) can still slip through.
Operator action: **give every server secret a recognizable name** — prefer
`HEADCOUNT1_…` for app config, keep provider/cloud creds under the cloud
prefixes, and avoid opaque names for secret values. The strongest isolation
remains the **dedicated sandbox uid**, which prevents reading the server's env
via `/proc` regardless of naming. (If you have an unavoidable differently-named
secret, extend `isServerSecretEnv` in `engine/aicli/tools/sandbox.go`.)

This layer is always on and needs no kernel support, but it only removes what it
recognizes.

---

## Step 8: Turn it on

| Env var | Default | Effect |
| ------- | ------- | ------ |
| `HEADCOUNT1_SANDBOX_UID` | `0` (off) | Run the agent shell as this uid. Requires `CAP_SETUID` and steps 3 & 5. |
| `HEADCOUNT1_SANDBOX_GID` | = UID | Primary gid for the sandbox process. |
| `HEADCOUNT1_SANDBOX_READ_SCOPING` | off | `1`/`true`/`yes`/`on` → hide `${HEADCOUNT1_HOME}` from reads (needs Landlock). |

Recommended hardened production profile: **both** the dedicated uid and
read-scoping, on a Landlock-capable host.

---

## Verification — prove it's actually on

Don't trust the config; confirm the kernel is enforcing. Have an agent run these
(or run them as the sandbox user):

```sh
# 1. Write sandbox: must FAIL outside the workspace
touch /etc/hc1-probe            # expected: Permission denied
touch ./hc1-probe && rm hc1-probe   # expected: OK (inside workspace)

# 2. Dedicated uid: the shell is the sandbox user
id                              # expected: uid=<HEADCOUNT1_SANDBOX_UID>

# 3. Secret files unreadable (uid mode and/or read-scoping)
cat "$HOME/../<server-user>/.headcount1/keyring.sealed"   # expected: Permission denied / No such file
cat /proc/1/environ            # (server pid) expected: Permission denied under a dedicated uid

# 4. Env scrub: no server secrets present
env | grep -Ei 'HEADCOUNT1_|VAULT_|AWS_|DATABASE_URL|SMTP_'   # expected: empty
```

Also confirm the backend at startup: the app builds a one-line summary via
`sandboxDescription()` such as
`Landlock (kernel ABI v5) — writes restricted to the workspace [+dedicated uid 8001, +read-scoped (home hidden)]`.
`DISABLED …` or `path validation only` means **no kernel enforcement** — fix
steps 1–2 before relying on it. (Emitting this at startup is handled by
`logSandboxMode()`.)

If a build breaks after enabling hardening, the usual causes map directly to the
steps: writes failing → step 5 (workspace ownership); `go/npm` cache errors →
step 3 (sandbox user's home) or step 5; "command not found" / can't read a
runtime → step 6 (toolchain under `$HOME`); privilege error at start → step 4
(`CAP_SETUID`).

---

## What the application does NOT do for you (explicit gaps)

- **It does not create the sandbox OS user** or its home.
- **It does not chown/relax workspace permissions** for the sandbox uid — dirs
  are made `0755` server-owned; you must make the workspace zone writable (step
  5). This is the most common cause of "hardening broke all my builds."
- **It does not fail-closed when the kernel can't enforce** — it degrades to
  unsandboxed (`BestEffort`). Verification is on you.
- **It does not tighten `${HEADCOUNT1_HOME}` directory modes** beyond the default
  `0755` dirs / `0600` secret files — read-scoping or a dedicated uid is what
  actually blocks reads; without them the `0755` dirs are readable.
- **It does not scrub unrecognized secret env names** — only the known
  prefixes/names (step 7).
- **It does not sandbox the network**, nor confine git/SSH operations the server
  itself runs (those run as the server uid, outside the bash sandbox).

---

## Platform support

| | Write sandbox | Dedicated uid | Read-scoping |
| --- | --- | --- | --- |
| **Linux (Landlock)** | ✅ | ✅ | ✅ |
| **macOS (Seatbelt)** | ✅ | ❌ | ❌ |
| **Windows / other** | ❌ (path validation only) | ❌ | ❌ |

The full secret-protection story is **Linux-only**. Run production on Linux with
Landlock.
