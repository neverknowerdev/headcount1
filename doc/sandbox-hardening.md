# Sandbox Hardening

How the agent's shell tool (`bash`) is confined so that a coding agent — which
runs model-authored commands — cannot escape its workspace, tamper with the rest
of the machine, or read the server's secrets.

All of the code lives in `engine/aicli/tools/`:

| File | Role |
| --- | --- |
| `exec_command.go` | The `bash` tool. Validates, scrubs the env, runs the command under the sandbox. |
| `sandbox.go` | Path validation, env scrubbing, the `sandboxHardening` config struct. |
| `sandbox_exec.go` | Cross-platform contract + the writable-dirs list (temp + toolchain caches). |
| `sandbox_exec_linux.go` | Landlock enforcement via self-re-exec. |
| `sandbox_exec_darwin.go` | macOS Seatbelt (`sandbox-exec`) enforcement. |
| `sandbox_exec_other.go` | Fallback (Windows, etc.): path validation only. |
| `sandbox_exec_nonlinux.go` | No-op `MaybeRunSandboxChild` off Linux. |

---

## Threat model

The agent's `bash` tool executes commands the LLM writes. We assume the command
string is *adversarial* — a prompt-injected or confused agent may try to read
`~/.headcount1/keystore.json`, dump the process environment, write outside the
workspace, or pivot to another tenant's data. The sandbox is what stands between
"the model asked to `cat` the master key" and that actually happening.

Three things must be protected:

1. **The rest of the filesystem** — the agent may only *write* inside its
   workspace (plus scratch/cache dirs).
2. **The server's at-rest secrets** — the SQLite DB, the `keystore.json` wrapped
   DEK, the graceful-exit keyring snapshot, and per-user SSH keys, all under the
   server's home directory.
3. **The server's in-process secrets** — the master/boot key, Vault/cloud
   credentials, `DATABASE_URL`, SMTP secrets, all present in the server's
   environment.

---

## Layers

The protection is built in layers, from a friendly UX guard down to kernel
enforcement. Two layers are **always on**; two are **operator opt-in** and off by
default, so an unconfigured single-user dev/CI box behaves exactly as it always
has.

```
 bash("rm -rf /etc")  ─┐
                       │
   ┌───────────────────▼────────────────────────────────┐
   │ 1. validateCommandPaths()   (always on, all OSes)   │  UX guard
   │    friendly early rejection of obvious escapes      │
   ├─────────────────────────────────────────────────────┤
   │ 2. scrubbedEnv()            (always on, all OSes)    │  secret env removed
   ├─────────────────────────────────────────────────────┤
   │ 3. kernel write sandbox     (always on, Linux/macOS) │  Landlock / Seatbelt
   │      + read-scoping         (opt-in, Linux only)     │  hide the home dir
   │      + dedicated uid        (opt-in, Linux only)     │  drop privileges
   └─────────────────────────────────────────────────────┘
```

### Layer 1 — Path validation (always on, every platform)

`validateCommandPaths` (`sandbox.go`) runs before every command on every OS. It
is a **UX layer, not the security boundary**: it gives the model a clear,
actionable error for an obvious escape *before* the command runs, instead of a
confusing mid-command "permission denied".

It rejects:

- `$HOME` / `${HOME}` variable references (they'd bypass textual path checks);
- absolute paths, `~`/`~/…` home paths, and `..` traversals that resolve outside
  the workspace (unless they land inside a configured read-only root);
- symlinks *inside* the workspace that point *outside* it — every referenced
  path that exists is re-checked through `os.Root`, which resolves it with the
  workspace as an inescapable root, so a sneaky symlink fails here even though
  the plain text check passed.

Because it's only a UX guard, a command that slips a path past it (e.g. one
constructed at runtime inside the shell) is still caught by the kernel layer.

### Layer 2 — Environment scrubbing (always on, every platform)

`exec_command.go` sets `cmd.Env = scrubbedEnv()`. `scrubbedEnv` (`sandbox.go`)
copies the process environment but drops every server secret, so `env`,
`/proc/self/environ`, or a leaky subprocess can't exfiltrate them. The agent
still gets a normal working environment (`PATH`, tool caches, project vars).

Dropped keys (`isServerSecretEnv`): any var prefixed `HEADCOUNT1_`, `VAULT_`,
`AWS_`, `AZURE_`, `GCP_`, `GOOGLE_`, plus `DATABASE_URL`, `SMTP_PASSWORD`,
`SMTP_USERNAME`, `SMTP_HOST`.

On Linux the Landlock re-exec child inherits this scrubbed set, so the confined
shell never sees the originals.

### Layer 3 — Kernel write sandbox (always on where supported)

Enforcement is kernel-level and per-platform. **Writes** are restricted to the
workspace plus a small set of scratch/cache directories; **reads** are
unrestricted by default (toolchains live all over the filesystem).

The extra writable directories (`extraWritableDirsForHome` in `sandbox_exec.go`)
are: `TMPDIR`, `/tmp`, `/var/tmp`, `/dev/shm`, and the toolchain caches
`~/.cache` (GOCACHE, pip, uv…), `~/.npm`, `~/go/pkg` (GOMODCACHE), plus
`~/Library/Caches` on macOS. Without these, `go build` / `npm install` would
fail under the write restriction.

| Platform | Backend | Notes |
| --- | --- | --- |
| Linux | Landlock LSM | Self-re-exec (below). Degrades to unsandboxed if the kernel lacks Landlock. |
| macOS | Seatbelt (`sandbox-exec`) | Generated profile; degrades to unsandboxed if `sandbox-exec` is missing. |
| Windows / other | none | `validateCommandPaths` is the only guard. |

If the kernel backend is unavailable the command runs **unsandboxed** — the same
as the historical behavior — rather than failing. `sandboxDescription()` reports
which mode is active (e.g. `Landlock (kernel ABI v5) — writes restricted to the
workspace [+dedicated uid 8001, +read-scoped (home hidden)]`), and
`logSandboxMode()` emits it once per process.

---

## How the Linux Landlock sandbox works (self-re-exec)

Landlock rules apply to the **calling** process and are inherited by all its
children — and Go can't run arbitrary code between `fork` and `exec`. So we can't
just fork, restrict, and exec. Instead the binary **re-executes itself**:

```
server process
  └─ exec.Command(self, "__headcount1-sandbox-child__", workspace, command, <cfg>)
        │  (SysProcAttr.Credential set here if a dedicated uid is configured)
        ▼
     re-exec child  ── MaybeRunSandboxChild() fires first thing in main() ──┐
        1. applies the Landlock ruleset to itself                           │
        2. syscall.Exec("sh", "-c", command)  ← replaces the process image  │
        ▼                                                                    │
     sh -c "<command>"   (keeps the child PID, so the parent's timeout/kill  │
                          still applies; inherits the Landlock restriction)  ┘
```

Key mechanics:

- **`MaybeRunSandboxChild()` must be the first thing `main()` does** (see
  `main.go:50`; test binaries call it in `TestMain`). It detects the hidden
  `__headcount1-sandbox-child__` marker in `argv[1]`; on a normal start it's a
  no-op.
- The child applies the ruleset with `landlock.V5.BestEffort()`, so it degrades
  gracefully on older Landlock ABIs (the parent already verified ABI ≥ 1 before
  re-executing).
- After `syscall.Exec`, the shell **keeps the child's PID**, so the parent's
  `context` timeout (60 s hard cap in `exec_command.go`) and kill still reach it.
- **Grant lists are computed in the parent, not the child.** The child may run
  as a *different* uid (see below) whose home dir and caches differ, so it can't
  recompute them. The parent serializes them into a base64 JSON `childConfig`
  (`argv[4]`: writable dirs, read-only roots, the read-scoping flag). A legacy
  4-arg invocation with no config means "no hardening, read everything."

The ruleset itself (`restrictWritesToWorkspace`):

- `RWDirs(workspace + writable…).WithRefer().IgnoreIfMissing()` — read+write in
  the workspace and scratch/cache dirs. `WithRefer` (Landlock ABI v2) allows
  `mv`/`ln` across directories within the writable trees. `IgnoreIfMissing`
  tolerates dirs that don't exist on this box.
- `RWFiles(/dev/null, /dev/zero, /dev/full, /dev/tty, /dev/ptmx, /dev/random,
  /dev/urandom).WithIoctlDev()` — device files shells routinely touch.
- Read grant: `RODirs("/")` (everything) by default, **or** the scoped root list
  when read-scoping is on.

---

## Opt-in hardening (off by default)

Two additional restrictions close the read paths to the server's at-rest and
in-process secrets. Both are **off by default** so dev/CI is unchanged, and both
are configured through env vars read by `loadSandboxHardening()` (`sandbox.go`).
They're Linux/Landlock-only.

### Dedicated sandbox UID/GID — `HEADCOUNT1_SANDBOX_UID` / `HEADCOUNT1_SANDBOX_GID`

Run the agent's shell as a dedicated, unprivileged user instead of the server's
own uid. The parent sets `SysProcAttr.Credential{Uid, Gid, NoSetGroups: true}`
on the re-exec (`sandbox_exec_linux.go`), so the shell and all its descendants
run as that uid. `NoSetGroups` avoids inheriting the server's supplementary
groups.

What this buys you:

- The server's secret files (SQLite DB, `keystore.json`, keyring snapshot, SSH
  keys) are `0600` and owned by the **server** uid, so a different sandbox uid
  simply can't read them — a filesystem-permission boundary underneath Landlock.
- The sandbox uid can't read the server's `/proc/<server-pid>/environ` either;
  the kernel restricts that to the owning uid. (Belt-and-suspenders with the
  always-on env scrub.)

Requirements & behavior:

- The server must hold **`CAP_SETUID`** (e.g. running as root) to drop
  privileges. If it doesn't, the command **fails loudly** rather than silently
  running with the server's privileges.
- `HEADCOUNT1_SANDBOX_GID` defaults to the uid's value when unset.
- Toolchain caches are resolved against **that uid's** home (`user.LookupId`),
  not the server's — otherwise `go build` / `npm install` would try to write the
  server-owned `~/.cache` and fail.

### Read scoping — `HEADCOUNT1_SANDBOX_READ_SCOPING` (`1`/`true`/`yes`/`on`)

Replace the default `RODirs("/")` "read everything" grant with an explicit
allowlist of system roots that **omits the server's home directory**. This blocks
reads of `~/.headcount1` (DB, keystore, keyring snapshot, SSH keys) **even when
the agent shares the server's uid** — so it's useful on its own or alongside a
dedicated uid.

The allowed roots (`readScopeRoots` in `sandbox_exec_linux.go`) are the system
directories a toolchain needs: `/usr`, `/bin`, `/sbin`, `/lib*`, `/etc`, `/opt`,
`/proc`, `/sys`, `/dev`, `/run`, `/var`, `/snap` — plus the configured read-only
roots and the already-readable workspace and caches. The home directory is
deliberately not on the list.

### Combining them

| `SANDBOX_UID` | `READ_SCOPING` | Home-dir secrets unreadable when the agent… |
| --- | --- | --- |
| unset | off | (baseline — readable) |
| set | off | …runs as another uid (file perms) |
| unset | on | …shares the server uid (Landlock read allowlist) |
| set | on | …in either case (defense in depth) |

For a hardened multi-tenant deployment, set both.

---

## Configuration reference

| Env var | Default | Effect |
| --- | --- | --- |
| `HEADCOUNT1_SANDBOX_UID` | `0` (off) | Run the agent shell as this uid. Requires `CAP_SETUID`. |
| `HEADCOUNT1_SANDBOX_GID` | = UID | Group for the dedicated uid. |
| `HEADCOUNT1_SANDBOX_READ_SCOPING` | off | `1`/`true`/`yes`/`on` → hide the home dir from reads (Landlock allowlist). |

Read-only roots (parent task workdir, artifacts dir, …) are passed
programmatically to the tools, not via env — see `DefaultRegistry(workspacePath,
readOnlyDirs...)` and `NewExecCommand`. They are readable but never writable.

---

## Limitations

- **Linux is the only platform with the full feature set.** Read-scoping and the
  dedicated uid are Landlock-only; macOS gets the write sandbox but neither
  extra; Windows/other get only path validation.
- **No kernel backend → no kernel enforcement.** On a kernel without Landlock (or
  a mac without `sandbox-exec`), commands run unsandboxed; only path validation
  applies. Check the `sandboxDescription()` line to confirm the active mode in
  production.
- **The write sandbox restricts writes, not reads.** Without read-scoping the
  agent can read anything the process uid can. Read-scoping and/or a dedicated
  uid are what close the read paths to secrets — enable them in production.
- **Path validation is not a boundary.** It's a UX convenience; the kernel layer
  is the real enforcement.
- **Network is not sandboxed here.** This document covers filesystem/privilege
  confinement only.
