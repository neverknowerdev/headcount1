#!/bin/sh
# Paperclip setup script — macOS
# Checks and installs all runtime dependencies.
# Exits 1 with a summary if any dependency could not be installed.

FAILED=""
SOFT_FAILED=""

add_failure() {
    echo "[setup] WARNING: $1 — $2"
    FAILED="${FAILED}
  • $1: $2"
}

# add_soft_failure is for optional dependencies (currently: gh CLI) whose
# absence should not block the app from starting — only surface a warning.
add_soft_failure() {
    echo "[setup] SOFT_FAIL: $1 — $2"
    SOFT_FAILED="${SOFT_FAILED}
  • $1: $2"
}

brew_install() {
    HOMEBREW_NO_AUTO_UPDATE=1 brew install "$1" >/dev/null 2>&1
}

# ── Homebrew (prerequisite for most installs) ─────────────────────────────────
if ! command -v brew >/dev/null 2>&1; then
    echo "[setup] Homebrew not found — some automatic installs will be skipped."
    echo "[setup] Install Homebrew from https://brew.sh to enable auto-install."
fi

# ── git ──────────────────────────────────────────────────────────────────────
if command -v git >/dev/null 2>&1; then
    echo "[setup] git: OK"
else
    echo "[setup] git: not found — installing via Homebrew..."
    if command -v brew >/dev/null 2>&1 && brew_install git && command -v git >/dev/null 2>&1; then
        echo "[setup] git: installed"
    else
        add_failure "git" "not found — install Xcode Command Line Tools (xcode-select --install) or brew install git"
    fi
fi

# ── python3 ──────────────────────────────────────────────────────────────────
if command -v python3 >/dev/null 2>&1; then
    echo "[setup] python3: OK"
else
    echo "[setup] python3: not found — installing via Homebrew..."
    if command -v brew >/dev/null 2>&1 && brew_install python3 && command -v python3 >/dev/null 2>&1; then
        echo "[setup] python3: installed"
    else
        add_failure "python3" "not found — install from https://python.org or: brew install python3"
    fi
fi

# ── pip ──────────────────────────────────────────────────────────────────────
if command -v python3 >/dev/null 2>&1; then
    if ! python3 -m pip --version >/dev/null 2>&1; then
        echo "[setup] pip: not found — trying ensurepip..."
        python3 -m ensurepip --upgrade >/dev/null 2>&1
        if ! python3 -m pip --version >/dev/null 2>&1; then
            add_failure "pip" "not available — run: python3 -m ensurepip --upgrade"
        else
            echo "[setup] pip: installed"
        fi
    else
        echo "[setup] pip: OK"
    fi
fi

# ── markitdown ───────────────────────────────────────────────────────────────
if command -v python3 >/dev/null 2>&1; then
    if python3 -c "from markitdown import MarkItDown" >/dev/null 2>&1; then
        echo "[setup] markitdown: OK"
    else
        echo "[setup] markitdown: not found — installing..."
        if python3 -m pip install --quiet markitdown >/dev/null 2>&1 && \
           python3 -c "from markitdown import MarkItDown" >/dev/null 2>&1; then
            echo "[setup] markitdown: installed"
        else
            add_failure "markitdown" "pip install failed — web_fetch markdown conversion will be unavailable"
        fi
    fi
fi

# ── Node.js / npm ────────────────────────────────────────────────────────────
if command -v npm >/dev/null 2>&1; then
    echo "[setup] npm: OK"
else
    echo "[setup] npm: not found — installing Node.js via Homebrew..."
    if command -v brew >/dev/null 2>&1 && brew_install node && command -v npm >/dev/null 2>&1; then
        echo "[setup] npm: installed"
    else
        add_failure "npm" "could not be installed — install Node.js from https://nodejs.org or: brew install node"
    fi
fi

# ── chromium ─────────────────────────────────────────────────────────────────
chromium_ok=0
for p in "/Applications/Chromium.app/Contents/MacOS/Chromium" \
         "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
         "/usr/local/bin/chromium" \
         "/opt/homebrew/bin/chromium"; do
    if [ -x "$p" ]; then
        chromium_ok=1
        echo "[setup] chromium: OK ($p)"
        break
    fi
done
if [ "$chromium_ok" -eq 0 ]; then
    echo "[setup] chromium: not found — installing via Homebrew..."
    if command -v brew >/dev/null 2>&1; then
        if HOMEBREW_NO_AUTO_UPDATE=1 brew install --cask chromium >/dev/null 2>&1 || \
           HOMEBREW_NO_AUTO_UPDATE=1 brew install --cask google-chrome >/dev/null 2>&1; then
            echo "[setup] chromium: installed"
        else
            add_failure "chromium" "could not be installed — install manually for browser_use support"
        fi
    else
        add_failure "chromium" "Homebrew not available — install Chromium from https://www.chromium.org for browser_use support"
    fi
fi

# ── gh CLI ───────────────────────────────────────────────────────────────────
if command -v gh >/dev/null 2>&1; then
    echo "[setup] gh CLI: OK"
else
    echo "[setup] gh CLI: not found — installing via Homebrew..."
    if command -v brew >/dev/null 2>&1 && brew_install gh && command -v gh >/dev/null 2>&1; then
        echo "[setup] gh CLI: installed"
    else
        add_soft_failure "gh CLI" "could not be installed — install via: brew install gh"
    fi
fi

# ── codegraph ────────────────────────────────────────────────────────────────
if command -v codegraph >/dev/null 2>&1; then
    echo "[setup] codegraph: OK"
else
    echo "[setup] codegraph: not found — installing via npm..."
    if command -v npm >/dev/null 2>&1 && \
       npm install -g @colbymchenry/codegraph >/dev/null 2>&1 && \
       command -v codegraph >/dev/null 2>&1; then
        echo "[setup] codegraph: installed"
    else
        add_failure "codegraph" "could not be installed — run: npm install -g @colbymchenry/codegraph"
    fi
fi

# ── summary ──────────────────────────────────────────────────────────────────
if [ -n "$SOFT_FAILED" ]; then
    printf '\n[setup] Some optional dependencies are missing or could not be installed:%s\n' "$SOFT_FAILED"
fi

if [ -n "$FAILED" ]; then
    printf '\n[setup] Some dependencies are missing or could not be installed:%s\n' "$FAILED" >&2
    exit 1
fi

echo "[setup] All dependencies OK"
