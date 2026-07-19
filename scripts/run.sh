#!/usr/bin/env bash
#
# scripts/run.sh — launch headcount1 locally with a PERSISTENT boot key, so a
# graceful restart (Ctrl+C, then start again) restores everyone's unlocked vault
# instead of asking each user to re-tap their passkey.
#
# The boot key MUST be the same value on every launch to decrypt the snapshot
# written at the last graceful shutdown. This script persists it in a 0600 env
# file next to your data so that "same value every launch" just happens. Losing
# that key is not catastrophic — it costs one passkey re-tap per active user,
# no data loss (see doc/boot-key.md).
#
# Usage:
#   scripts/run.sh [--build] [--port N] [--no-boot-key] [--regenerate-boot-key]
#
#   --build                 build a binary and run it (default: `go run .`)
#   --port N                listen on port N (default: 8080)
#   --no-boot-key           run without a boot key (restarts will require a re-tap)
#   --regenerate-boot-key   mint a fresh boot key (invalidates any pending snapshot)
#   -h, --help              show this help
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_DIR="${HEADCOUNT1_HOME:-$HOME/.headcount1}"
ENV_FILE="${HEADCOUNT1_ENV_FILE:-$DATA_DIR/headcount1.env}"

BUILD=0
NO_BOOT_KEY=0
REGEN=0
PORT_OVERRIDE=""

usage() { sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [ $# -gt 0 ]; do
	case "$1" in
		--build) BUILD=1 ;;
		--no-boot-key) NO_BOOT_KEY=1 ;;
		--regenerate-boot-key) REGEN=1 ;;
		--port) shift; PORT_OVERRIDE="${1:-}" ;;
		--port=*) PORT_OVERRIDE="${1#*=}" ;;
		-h|--help) usage 0 ;;
		*) echo "unknown option: $1" >&2; usage 1 ;;
	esac
	shift
done

# gen_key prints 64 hex chars (32 random bytes), preferring openssl.
gen_key() {
	if command -v openssl >/dev/null 2>&1; then
		openssl rand -hex 32
	else
		# Fallback: read /dev/urandom without openssl.
		od -An -tx1 -N32 /dev/urandom | tr -d ' \n'
	fi
}

# set_env_var upserts NAME=VALUE into the 0600 env file.
set_env_var() {
	local name="$1" val="$2"
	mkdir -p "$(dirname "$ENV_FILE")"
	touch "$ENV_FILE"
	chmod 600 "$ENV_FILE"
	if grep -q "^${name}=" "$ENV_FILE" 2>/dev/null; then
		grep -v "^${name}=" "$ENV_FILE" > "$ENV_FILE.tmp" && mv "$ENV_FILE.tmp" "$ENV_FILE"
	fi
	printf '%s=%s\n' "$name" "$val" >> "$ENV_FILE"
	chmod 600 "$ENV_FILE"
}

mkdir -p "$DATA_DIR"
# Lock the data dir to the owner (0700). The secret files inside are already
# 0600, but this stops other local users from even traversing in. Note: a
# dedicated-sandbox-uid production setup needs a different mode (see
# doc/sandbox-hardening.md) — this 0700 is for the local single-user path.
chmod 700 "$DATA_DIR" 2>/dev/null || true

# Load any previously-persisted config (this is what makes the boot key stable
# across launches).
if [ -f "$ENV_FILE" ]; then
	set -a
	# shellcheck disable=SC1090
	. "$ENV_FILE"
	set +a
fi

# ── boot key selection ────────────────────────────────────────────────────────
if [ -n "${VAULT_ADDR:-}" ]; then
	echo "→ Boot key: Vault Transit (VAULT_ADDR set). Seamless restarts enabled."
elif [ "$NO_BOOT_KEY" = 1 ]; then
	unset HEADCOUNT1_BOOT_KEY || true
	echo "→ Boot key: DISABLED (--no-boot-key). A restart will require each active user to re-tap their passkey."
elif [ "$REGEN" = 1 ] || [ -z "${HEADCOUNT1_BOOT_KEY:-}" ]; then
	do_generate=1
	if [ "$REGEN" != 1 ] && [ -t 0 ]; then
		# Interactive first-run prompt.
		printf 'Enable seamless restarts (skip the passkey re-tap after a graceful restart)? [Y/n] '
		read -r reply || reply=""
		case "$reply" in
			[nN]*) do_generate=0 ;;
		esac
	fi
	if [ "$do_generate" = 1 ]; then
		key="$(gen_key)"
		set_env_var HEADCOUNT1_BOOT_KEY "$key"
		export HEADCOUNT1_BOOT_KEY="$key"
		if [ "$REGEN" = 1 ]; then
			echo "→ Boot key: REGENERATED and saved to $ENV_FILE (0600)."
			echo "  Any pending keyring.sealed from the previous key is now unreadable → one re-tap expected."
		else
			echo "→ Boot key: generated and saved to $ENV_FILE (0600). Reused on every launch from here on."
		fi
		echo "  Keep this file: losing it costs one passkey re-tap per user (no data loss). Never commit it."
		echo "  This stores the key next to your data — fine for local dev, NOT for production (see doc/boot-key.md)."
	else
		unset HEADCOUNT1_BOOT_KEY || true
		echo "→ Boot key: none. Restarts will require a passkey re-tap. Re-run to enable, or set HEADCOUNT1_BOOT_KEY."
	fi
else
	echo "→ Boot key: using the persisted key from $ENV_FILE. Seamless restarts enabled."
fi

# ── port ──────────────────────────────────────────────────────────────────────
if [ -n "$PORT_OVERRIDE" ]; then
	export PORT="$PORT_OVERRIDE"
fi
echo "→ Listening on port ${PORT:-8080}."

# ── launch ────────────────────────────────────────────────────────────────────
cd "$REPO_ROOT"
echo "→ Starting headcount1. Stop with Ctrl+C (graceful → keyring snapshot is written)."
echo

if [ "$BUILD" = 1 ]; then
	make build
	exec ./agent-orchestrator
else
	exec go run .
fi
