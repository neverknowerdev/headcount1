# Boot Key & Seamless Restarts

headcount1 is zero-knowledge: a user's secrets are decryptable only while that
user is signed in, because their data-encryption key (DEK) lives only in an
in-memory keyring, unlocked by their passkey. A plain process restart therefore
loses every unlocked DEK, and each active user has to re-tap their passkey.

The **boot key** is what lets a *planned* restart skip that re-tap.

---

## What the boot key does

On a **graceful** shutdown the server seals the in-memory keyring under the boot
key and writes it to `${HEADCOUNT1_HOME}/keyring.sealed`. On the next boot it
reads that file, unseals it with the boot key, restores the keyring, and deletes
the file. Net effect: a seamless deploy/restart with no passkey re-tap.

Without a boot key, none of that happens — the seal on shutdown and the restore
on boot are both skipped — so every restart requires a re-tap.

The boot key is selected in this order:

1. `VAULT_ADDR` set → HashiCorp Vault **Transit** (the recommended production path — the key never lives on the box).
2. `HEADCOUNT1_BOOT_KEY` set → an env-provided AES-256 key: 64 hex chars, or any passphrase (stretched with Argon2id). Persistent and externally managed.
3. **default** → a **self-managed local key** (see below): zero-config, generated per boot, on disk only while the server is stopped.
4. `HEADCOUNT1_LOCAL_BOOTKEY=0` (or `false`/`no`/`off`) → no boot key at all; every restart requires a re-tap.

**Why 3 is the default.** A deploy is a graceful restart, and a graceful restart
is supposed to come back with everyone still unlocked. Making that depend on an
env var nobody had set meant the feature was silently off on every box that
hadn't opted in — including staging. Option 4 keeps the strictest posture (never
any key material on disk, a re-tap after every restart) for a deployment that
wants it, and options 1–2 keep the key off the box entirely while still
re-warming.

### Self-managed local key (the default; disable with `HEADCOUNT1_LOCAL_BOOTKEY=0`)

Usually you don't want to manage a persistent key at all. In
this mode the server generates a **random boot key held only in memory** and
writes it to `${HEADCOUNT1_HOME}/keyring.bootkey` **only at graceful shutdown** —
right next to the `keyring.sealed` snapshot it seals. The next startup reads the
key, restores the keyring, and **deletes both files**. So, just like the
snapshot, no boot-key material sits on disk while the server is running; the two
files exist only during the window between a graceful stop and the next start.

The trade-off is deliberate: during that offline window the key and the
ciphertext are on disk *together*, so anyone who can read the disk **then** can
decrypt the snapshot. An external boot key (options 1–2) avoids that — the key is
never on the box, so the at-rest snapshot is useless on its own — and is still
what a production host should use. While the server is running, this mode leaves
nothing on disk either way.

---

## Your two questions

**Must it be the same on every launch?** **Yes.** The snapshot written at the
last graceful shutdown is encrypted under the boot key that was active then.
Boot with a *different* (or missing) boot key and `keyring.sealed` won't
decrypt, so the keyring isn't restored and users re-tap. (The file is discarded
after the attempt either way — it's never left lying around decryptable.)

**If I lose the boot key, do users have to re-tap?** **Yes — and that's the only
consequence. No data is lost.** The boot key protects a *transient convenience
snapshot*, nothing else. Losing it costs you exactly one passkey tap per active
user on the next restart. Rotating it is safe: set a new value, and from the
next graceful shutdown onward the new key is in effect (the one restart spanning
the change just needs a re-tap).

---

## There is no server-held master key

headcount1 deliberately has **no master key**. A secret at rest is sealed only
under its owning user's DEK, and that DEK exists only in memory while the user is
signed in (unlocked by their passkey's PRF). Nothing on the box — no
`master.key`, no `keystore.json`, no KMS-wrapped root key — can decrypt a user's
secrets when they are logged out. This is what makes the system zero-knowledge:
compromising the server at rest yields only ciphertext.

The boot key is the *only* server-side key material, and it is strictly an
availability convenience — it protects the **transient** `keyring.sealed` restart
snapshot and nothing else:

| | **Boot key** |
| --- | --- |
| Env / source | Vault Transit, `HEADCOUNT1_BOOT_KEY`, or the self-managed local key (default) |
| Protects | the **transient** `keyring.sealed` restart snapshot only |
| If lost / changed | users **re-tap once**; **no data loss** |
| Default when unset | self-managed local key — restarts re-warm; `HEADCOUNT1_LOCAL_BOOTKEY=0` opts out |

Rule of thumb: **treat the boot key as an availability convenience you can rotate
freely — there is nothing else to guard, because no server-held key can open a
signed-out user's secrets.**

---

## When the snapshot is (and isn't) written

- **Written** on a *graceful* exit — `SIGINT` (Ctrl+C) or `SIGTERM`. This is the
  normal `docker stop`, `systemctl stop`, Kubernetes pod termination, and a
  plain Ctrl+C on `go run`.
- **Not written** on a hard kill — `kill -9` / `SIGKILL`, OOM, power loss, or a
  panic. Nothing is left behind, so users re-tap. This is intentional: an
  *unexpected* death must leak nothing.
- **Dev hot-reloaders**: `make run-dev` is wired for the seamless path — it
  enables the self-managed local boot key (`HEADCOUNT1_LOCAL_BOOTKEY=1`) and its
  `.air.toml` sets `send_interrupt = true` + a `kill_delay`, so `air` sends
  `SIGINT` and the server has time to seal on every rebuild and on Ctrl+C. A
  raw hot-reloader that `SIGKILL`s the process (e.g. `air` with
  `send_interrupt = false`, `reflex`) still skips the snapshot and forces a
  re-tap; use `make run-dev`, `make run`, or `scripts/run.sh` for the seamless
  path.

---

## Security note — where to keep the boot key

Whoever holds **the boot key and a copy of `keyring.sealed` at the same time**
can reopen the vaults it contains. So the boot key is only as strong as where
you keep it:

- **Production**: inject it at boot from a secrets manager / KMS, or use Vault
  Transit (`VAULT_ADDR`) so the key never touches the box. Do **not** write it
  to a file next to the data.
- **Everywhere else (the default)**: the self-managed local key keeps the key in
  memory and puts it on disk only during the stopped window (see above). It
  deliberately weakens the guarantee against disk theft *while the server is
  stopped*, which is why a production host should still inject an external key —
  but it never leaves key material on disk while the server runs.

**On `0600` and "who can read it".** `0600` restricts by *UID*, not by process:
another user can't read it and neither can the agent under a dedicated sandbox
uid, but **any process running as your own user can** at the OS level. The
agent's `bash` tool runs as the server's uid, but its kernel sandbox now
**hides the whole data root** (`${HEADCOUNT1_HOME}`) from it by default,
re-granting only the current task's own dirs — so the DB, `ssh/`, `credentials/`,
`backups/`, `keyring.sealed`, and `keyring.bootkey` are all unreadable to the
agent even under a shared uid (see `doc/sandbox-hardening.md`). That closes the
agent path specifically; other same-uid processes are still bounded only by
`0600`.
In the self-managed local mode there is no boot-key file on disk *while the
server runs* — and there is no `master.key`/`keystore.json` to worry about at all
(the SQLite DB holds only per-user ciphertext no server key can open). The
remaining at-rest files (the SQLite DB, materialized SSH keys) carry the same
`0600`/same-UID exposure. To keep the agent out of all of them, enable
`HEADCOUNT1_SANDBOX_READ_SCOPING` (hides `${HEADCOUNT1_HOME}` from reads) and/or
`HEADCOUNT1_SANDBOX_UID` (runs the agent as a different uid) — see
`doc/sandbox-hardening.md`. In production, prefer never writing the boot key to
disk at all (KMS/Vault Transit).

Both `keyring.sealed` and the self-managed `keyring.bootkey` are short-lived —
written only at graceful shutdown and deleted at the next boot. The snapshot is
useless without the key; the key is useless without the snapshot; the risk is
only if *both* are read during the stopped window.

---

## Quick start

```bash
# Local/dev — zero-config, no key on disk while running (self-managed local key):
scripts/run.sh            # Ctrl+C to stop → snapshot + key written; next start restores + deletes both

# Or an explicit external key (persistent; also the production shape):
export HEADCOUNT1_BOOT_KEY=$(openssl rand -hex 32)
make run
```

## Reading the logs

Every boot says which branch of the above it took, so a surprise re-tap can be
diagnosed from the log alone:

```
Restored N unlocked vault(s) from graceful-exit snapshot (boot key: local:self-managed (…/keyring.bootkey))
Boot key: <backend> — no graceful-exit keyring snapshot to restore (previous exit was not graceful, or nobody was unlocked).
Boot key: none — the keyring is not sealed on shutdown, so every restart requires a passkey re-tap. (…)
```

and every shutdown says whether it left anything for the next boot:

```
Sealed N unlocked vault(s) for the next start (boot key: …).
Nothing to seal on shutdown: no vault is unlocked (boot key: …).
Boot key: none — not sealing the keyring; …
```

The same state is on `GET /api/deploy/status` as `boot_key`
(`{backend, snapshot_found, restored_vaults}`), for an operator with the global
admin API enabled.
