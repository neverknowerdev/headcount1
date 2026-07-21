#!/usr/bin/env bash
#
# scripts/run.sh — launch headcount1 locally with a self-managed boot key, so a
# graceful restart (Ctrl+C, then start again) restores everyone's unlocked vault
# instead of asking each user to re-tap their passkey — WITHOUT leaving any key
# material on disk while the server runs.
#
# It sets HEADCOUNT1_LOCAL_BOOTKEY=1, which makes the server hold a random boot
# key in memory and write it to disk (${HEADCOUNT1_HOME}/keyring.bootkey) ONLY at
# graceful shutdown, next to the keyring snapshot it seals — then consume and
# delete both at the next startup. Nothing persists on disk during runtime.
# Local/dev only; production uses an external boot key (see doc/boot-key.md).
#
# Usage:
#   scripts/run.sh [--build] [--port N] [--no-boot-key]
#
#   --build         build a binary and run it (default: `go run .`)
#   --port N        listen on port N (default: 8080)
#   --no-boot-key   don't seal the keyring on exit (restarts require a re-tap)
#   -h, --help      show this help
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_DIR="${HEADCOUNT1_HOME:-$HOME/.headcount1}"

BUILD=0
NO_BOOT_KEY=0
PORT_OVERRIDE=""

usage() { sed -n '2,19p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [ $# -gt 0 ]; do
	case "$1" in
		--build) BUILD=1 ;;
		--no-boot-key) NO_BOOT_KEY=1 ;;
		--port) shift; PORT_OVERRIDE="${1:-}" ;;
		--port=*) PORT_OVERRIDE="${1#*=}" ;;
		-h|--help) usage 0 ;;
		*) echo "unknown option: $1" >&2; usage 1 ;;
	esac
	shift
done

mkdir -p "$DATA_DIR"
# Lock the data dir to the owner (0700). The secret files inside are already
# 0600; this stops other local users from even traversing in. (A dedicated-
# sandbox-uid production setup needs a different mode — see doc/sandbox-hardening.md.)
chmod 700 "$DATA_DIR" 2>/dev/null || true

# ── boot key selection ────────────────────────────────────────────────────────
if [ -n "${VAULT_ADDR:-}" ]; then
	echo "→ Boot key: Vault Transit (VAULT_ADDR set). Seamless restarts enabled."
elif [ -n "${HEADCOUNT1_BOOT_KEY:-}" ]; then
	echo "→ Boot key: external HEADCOUNT1_BOOT_KEY (already set). Seamless restarts enabled."
elif [ "$NO_BOOT_KEY" = 1 ]; then
	unset HEADCOUNT1_LOCAL_BOOTKEY || true
	echo "→ Boot key: DISABLED (--no-boot-key). A restart will require each active user to re-tap their passkey."
else
	export HEADCOUNT1_LOCAL_BOOTKEY=1
	echo "→ Boot key: self-managed local key (HEADCOUNT1_LOCAL_BOOTKEY=1)."
	echo "  The key lives only in memory; it's written to $DATA_DIR/keyring.bootkey"
	echo "  ONLY at graceful shutdown and consumed+deleted at the next startup —"
	echo "  nothing on disk while running. Local/dev only (see doc/boot-key.md)."
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
