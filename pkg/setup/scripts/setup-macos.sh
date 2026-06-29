#!/bin/sh
set -e

# ── python3 ──────────────────────────────────────────────────────────────────
if ! command -v python3 >/dev/null 2>&1; then
    echo "[setup] python3 not found — attempting to install via Homebrew..."
    if command -v brew >/dev/null 2>&1; then
        brew install python3
    else
        echo "[setup] ERROR: python3 not found and Homebrew is not available." >&2
        echo "[setup] Please install Python 3 from https://python.org or install Homebrew first." >&2
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
