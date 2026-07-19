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

The boot key comes from `bootkey.FromEnv()`:

1. `VAULT_ADDR` set → HashiCorp Vault **Transit** (the recommended production path — the key never lives on the box).
2. `HEADCOUNT1_BOOT_KEY` set → an env-provided AES-256 key: 64 hex chars, or any passphrase (stretched with Argon2id).
3. neither → no boot key; restarts require a re-tap.

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

## Boot key vs. master key — don't confuse them

They are different keys with very different stakes:

| | **Master key** | **Boot key** |
| --- | --- | --- |
| Env / source | `HEADCOUNT1_MASTER_KEY`, `~/.headcount1/master.key`, or Vault | `HEADCOUNT1_BOOT_KEY` or Vault Transit |
| Protects | secrets **at rest** — wraps the data key in `keystore.json` | the **transient** `keyring.sealed` restart snapshot only |
| If lost / changed | at-rest data sealed under it becomes **unreadable** (a fingerprint guard refuses to boot rather than silently fail) — this is the serious one | users **re-tap once**; **no data loss** |
| Default when unset | auto-generated `master.key` (0600) is created and reused | none — restarts require a re-tap |

Rule of thumb: **guard the master key like the crown jewels; treat the boot key
as an availability convenience you can rotate freely.**

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
- **Local dev**: `scripts/run.sh` will, with your consent, persist a boot key in
  `${HEADCOUNT1_HOME}/headcount1.env` (0600) so restarts stay seamless. That
  keeps the key next to the data, which is fine for a single-user dev box but
  deliberately weakens the zero-knowledge guarantee against full-disk theft —
  don't do it on a shared/production host.

**On `0600` and "who can read it".** `0600` restricts by *UID*, not by process:
another user can't read it and neither can the agent under a dedicated sandbox
uid, but **any process running as your own user can** — including the agent's
`bash` tool, which by default runs as the server's uid with unrestricted reads.
The boot-key file is no more exposed than `master.key`, `keystore.json`, and
`keyring.sealed`, which already sit in the same directory at `0600`. To keep the
agent out of all of them, enable `HEADCOUNT1_SANDBOX_READ_SCOPING` (hides
`${HEADCOUNT1_HOME}` from reads) and/or `HEADCOUNT1_SANDBOX_UID` (runs the agent
as a different uid) — see `doc/sandbox-hardening.md`. In production, prefer never
writing the key to disk at all (KMS/Vault Transit).

`keyring.sealed` itself is short-lived (deleted on the next boot) and useless
without the boot key, so it is safe to leave on disk between a graceful stop and
the following start.

---

## Quick start

```bash
# One-off: enable seamless restarts for a local run.
export HEADCOUNT1_BOOT_KEY=$(openssl rand -hex 32)
make run          # Ctrl+C to stop → keyring.sealed written; next start restores it

# Or let the launch script persist the key for you and prompt on first run:
scripts/run.sh
```

On a successful restore you'll see, at startup:

```
Restored N unlocked vault(s) from graceful-exit snapshot (boot key: env:HEADCOUNT1_BOOT_KEY)
```
