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

Without a boot key configured, none of that happens — the seal on shutdown and
the restore on boot are both skipped — so every restart requires a re-tap. That
is the safe default: a machine with no external key material must not be able to
silently reopen users' vaults.

The boot key is selected in this order:

1. `VAULT_ADDR` set → HashiCorp Vault **Transit** (the recommended production path — the key never lives on the box).
2. `HEADCOUNT1_BOOT_KEY` set → an env-provided AES-256 key: 64 hex chars, or any passphrase (stretched with Argon2id). Persistent and externally managed.
3. else `HEADCOUNT1_LOCAL_BOOTKEY=1` → a **self-managed local key** (see below) — zero-config, for a single-user/dev box.
4. none of the above → no boot key; every restart requires a re-tap.

### Self-managed local key (`HEADCOUNT1_LOCAL_BOOTKEY=1`)

For a local/dev box you usually don't want to manage a persistent key at all. In
this mode the server generates a **random boot key held only in memory** and
writes it to `${HEADCOUNT1_HOME}/keyring.bootkey` **only at graceful shutdown** —
right next to the `keyring.sealed` snapshot it seals. The next startup reads the
key, restores the keyring, and **deletes both files**. So, just like the
snapshot, no boot-key material sits on disk while the server is running; the two
files exist only during the window between a graceful stop and the next start.

The trade-off is deliberate and is exactly why this mode is **local/dev only**:
during that offline window the key and the ciphertext are on disk *together*, so
anyone who can read the disk then can decrypt the snapshot. An external boot key
(options 1–2) avoids that — the key is never on the box, so the at-rest snapshot
is useless on its own. Use `scripts/run.sh`, which enables this mode by default.

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
| Env / source | `HEADCOUNT1_BOOT_KEY`, `HEADCOUNT1_LOCAL_BOOTKEY=1`, or Vault Transit |
| Protects | the **transient** `keyring.sealed` restart snapshot only |
| If lost / changed | users **re-tap once**; **no data loss** |
| Default when unset | none — restarts require a re-tap |

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
- **Watch out for dev hot-reloaders** (`air`, `reflex`, `make run-dev`): they
  usually `SIGKILL` the process on reload, so the snapshot is *not* written and
  you'll re-tap. Use `make run` (or `scripts/run.sh`) if you want to exercise
  the seamless-restart path locally.

---

## Security note — where to keep the boot key

Whoever holds **the boot key and a copy of `keyring.sealed` at the same time**
can reopen the vaults it contains. So the boot key is only as strong as where
you keep it:

- **Production**: inject it at boot from a secrets manager / KMS, or use Vault
  Transit (`VAULT_ADDR`) so the key never touches the box. Do **not** write it
  to a file next to the data.
- **Local dev**: `scripts/run.sh` enables the self-managed local key
  (`HEADCOUNT1_LOCAL_BOOTKEY=1`), which keeps the key in memory and puts it on
  disk only during the stopped window (see above). That's fine for a single-user
  dev box but deliberately weakens the guarantee against disk theft *while the
  server is stopped* — don't use it on a shared/production host.

**On `0600` and "who can read it".** `0600` restricts by *UID*, not by process:
another user can't read it and neither can the agent under a dedicated sandbox
uid, but **any process running as your own user can** — including the agent's
`bash` tool, which by default runs as the server's uid with unrestricted reads.
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

On a successful restore you'll see, at startup:

```
Restored N unlocked vault(s) from graceful-exit snapshot (boot key: local:self-managed (…/keyring.bootkey))
```
