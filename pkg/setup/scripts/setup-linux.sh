#!/bin/sh
set -e

# ── python3 ──────────────────────────────────────────────────────────────────
if ! command -v python3 >/dev/null 2>&1; then
    echo "[setup] python3 not found — attempting to install..."
    if command -v apt-get >/dev/null 2>&1; then
        apt-get install -y python3 python3-pip
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y python3 python3-pip
    elif command -v yum >/dev/null 2>&1; then
        yum install -y python3 python3-pip
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm python python-pip
    else
        echo "[setup] ERROR: python3 not found and no supported package manager available (apt-get, dnf, yum, pacman)" >&2
        exit 1
    fi
fi

# ── pip3 / pip ───────────────────────────────────────────────────────────────
if ! command -v pip3 >/dev/null 2>&1 && ! python3 -m pip --version >/dev/null 2>&1; then
    echo "[setup] pip not found — attempting ensurepip..."
    python3 -m ensurepip --upgrade || true
fi

# ── markitdown ───────────────────────────────────────────────────────────────
if ! python3 -c "from markitdown import MarkItDown" >/dev/null 2>&1; then
    echo "[setup] markitdown not found — installing..."
    python3 -m pip install --quiet markitdown
    if ! python3 -c "from markitdown import MarkItDown" >/dev/null 2>&1; then
        echo "[setup] ERROR: markitdown install reported success but import still fails" >&2
        exit 1
    fi
fi

echo "[setup] All dependencies OK"
