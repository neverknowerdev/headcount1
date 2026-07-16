#!/bin/sh
# Headcount1 setup script — macOS
# Checks and installs all runtime dependencies.
# Exits 1 with a summary if any dependency could not be installed.

FAILED=""
SOFT_FAILED=""
DETAILS=""

# add_failure records a blocking dependency failure. $3, if given, is the raw
# command output that explains why the install failed — surfaced in the UI
# as an expandable detail alongside the summary reason in $2.
add_failure() {
    echo "[setup] WARNING: $1 — $2"
    FAILED="${FAILED}
  • $1: $2"
    if [ -n "$3" ]; then
        _detail_id=$(printf '%s' "$1" | tr ' ' '_')
        DETAILS="${DETAILS}
[setup] DETAIL_BEGIN $_detail_id
$3
[setup] DETAIL_END"
    fi
}

# add_soft_failure is for optional dependencies (currently: gh CLI) whose
# absence should not block the app from starting — only surface a warning.
add_soft_failure() {
    echo "[setup] SOFT_FAIL: $1 — $2"
    SOFT_FAILED="${SOFT_FAILED}
  • $1: $2"
    if [ -n "$3" ]; then
        _detail_id=$(printf '%s' "$1" | tr ' ' '_')
        DETAILS="${DETAILS}
[setup] DETAIL_BEGIN $_detail_id
$3
[setup] DETAIL_END"
    fi
}

brew_install() {
    HOMEBREW_NO_AUTO_UPDATE=1 brew install "$1" 2>&1
}

# step announces what setup is currently working on. The Go side watches the
# script's output for these lines and shows the message in the UI loading
# screen, so emit one before anything that might take a while.
step() {
    echo "[setup] STEP: $1"
}

# ── Homebrew (prerequisite for most installs) ─────────────────────────────────
if ! command -v brew >/dev/null 2>&1; then
    echo "[setup] Homebrew not found — some automatic installs will be skipped."
    echo "[setup] Install Homebrew from https://brew.sh to enable auto-install."
fi

# ── git ──────────────────────────────────────────────────────────────────────
step "Checking git"
if command -v git >/dev/null 2>&1; then
    echo "[setup] git: OK"
else
    step "Installing git via Homebrew"
    echo "[setup] git: not found — installing via Homebrew..."
    if command -v brew >/dev/null 2>&1; then
        install_output=$(brew_install git)
    else
        install_output="Homebrew not available — install Xcode Command Line Tools (xcode-select --install) or brew install git"
    fi
    if command -v git >/dev/null 2>&1; then
        echo "[setup] git: installed"
    else
        add_failure "git" "not found — install Xcode Command Line Tools (xcode-select --install) or brew install git" "$install_output"
    fi
fi

# ── python3 ──────────────────────────────────────────────────────────────────
step "Checking python3"
if command -v python3 >/dev/null 2>&1; then
    echo "[setup] python3: OK"
else
    step "Installing python3 via Homebrew"
    echo "[setup] python3: not found — installing via Homebrew..."
    if command -v brew >/dev/null 2>&1; then
        install_output=$(brew_install python3)
    else
        install_output="Homebrew not available — install from https://python.org"
    fi
    if command -v python3 >/dev/null 2>&1; then
        echo "[setup] python3: installed"
    else
        add_failure "python3" "not found — install from https://python.org or: brew install python3" "$install_output"
    fi
fi

# ── pip ──────────────────────────────────────────────────────────────────────
step "Checking pip"
if command -v python3 >/dev/null 2>&1; then
    if ! python3 -m pip --version >/dev/null 2>&1; then
        step "Installing pip"
        echo "[setup] pip: not found — trying ensurepip..."
        install_output=$(python3 -m ensurepip --upgrade 2>&1)
        if ! python3 -m pip --version >/dev/null 2>&1; then
            add_failure "pip" "not available — run: python3 -m ensurepip --upgrade" "$install_output"
        else
            echo "[setup] pip: installed"
        fi
    else
        echo "[setup] pip: OK"
    fi
fi

# ── markitdown ───────────────────────────────────────────────────────────────
# Installed into a dedicated virtualenv (per markitdown's own docs, which
# recommend a venv) rather than the system/Homebrew Python. A venv is never
# "externally managed" under PEP 668, so this sidesteps that guard entirely
# and never risks upgrading some shared Homebrew-managed dependency.
VENV_DIR="${HEADCOUNT1_VENV_DIR:-$HOME/.headcount1/venv}"
step "Checking markitdown"
if command -v python3 >/dev/null 2>&1; then
    if [ ! -x "$VENV_DIR/bin/python3" ]; then
        step "Creating Python virtualenv"
        venv_output=$(python3 -m venv "$VENV_DIR" 2>&1)
    fi
    if [ ! -x "$VENV_DIR/bin/python3" ]; then
        add_failure "markitdown" "could not create virtualenv at $VENV_DIR — web_fetch markdown conversion will be unavailable" "$venv_output"
    elif "$VENV_DIR/bin/python3" -c "from markitdown import MarkItDown" >/dev/null 2>&1; then
        echo "[setup] markitdown: OK"
    else
        step "Installing markitdown"
        echo "[setup] markitdown: not found — installing..."
        install_output=$("$VENV_DIR/bin/python3" -m pip install markitdown 2>&1)
        if "$VENV_DIR/bin/python3" -c "from markitdown import MarkItDown" >/dev/null 2>&1; then
            echo "[setup] markitdown: installed"
        else
            reason="pip install failed — web_fetch markdown conversion will be unavailable"
            pyver=$("$VENV_DIR/bin/python3" -c 'import sys; print("%d.%d" % sys.version_info[:2])' 2>/dev/null)
            case "$pyver" in
                2.*|3.0|3.1|3.2|3.3|3.4|3.5|3.6|3.7|3.8|3.9)
                    reason="python $pyver is too old — markitdown requires Python >=3.10; brew install python3 and retry"
                    ;;
            esac
            add_failure "markitdown" "$reason" "$install_output"
        fi
    fi
fi

# ── Node.js / npm ────────────────────────────────────────────────────────────
step "Checking npm"
if command -v npm >/dev/null 2>&1; then
    echo "[setup] npm: OK"
else
    step "Installing Node.js via Homebrew"
    echo "[setup] npm: not found — installing Node.js via Homebrew..."
    if command -v brew >/dev/null 2>&1; then
        install_output=$(brew_install node)
    else
        install_output="Homebrew not available — install Node.js from https://nodejs.org"
    fi
    if command -v npm >/dev/null 2>&1; then
        echo "[setup] npm: installed"
    else
        add_failure "npm" "could not be installed — install Node.js from https://nodejs.org or: brew install node" "$install_output"
    fi
fi

# ── chromium ─────────────────────────────────────────────────────────────────
step "Checking chromium"
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
    step "Installing Chromium via Homebrew"
    echo "[setup] chromium: not found — installing via Homebrew..."
    if command -v brew >/dev/null 2>&1; then
        install_output=$(HOMEBREW_NO_AUTO_UPDATE=1 brew install --cask chromium 2>&1)
        if ! command -v chromium >/dev/null 2>&1 && [ ! -x "/Applications/Chromium.app/Contents/MacOS/Chromium" ]; then
            more_output=$(HOMEBREW_NO_AUTO_UPDATE=1 brew install --cask google-chrome 2>&1)
            install_output="${install_output}
${more_output}"
        fi
        if [ -x "/Applications/Chromium.app/Contents/MacOS/Chromium" ] || \
           [ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ] || \
           command -v chromium >/dev/null 2>&1; then
            echo "[setup] chromium: installed"
        else
            add_failure "chromium" "could not be installed — install manually for browser_use support" "$install_output"
        fi
    else
        add_failure "chromium" "Homebrew not available — install Chromium from https://www.chromium.org for browser_use support" ""
    fi
fi

# ── gh CLI ───────────────────────────────────────────────────────────────────
step "Checking gh CLI"
if command -v gh >/dev/null 2>&1; then
    echo "[setup] gh CLI: OK"
else
    step "Installing gh CLI via Homebrew"
    echo "[setup] gh CLI: not found — installing via Homebrew..."
    if command -v brew >/dev/null 2>&1; then
        install_output=$(brew_install gh)
    else
        install_output="Homebrew not available — install via: brew install gh"
    fi
    if command -v gh >/dev/null 2>&1; then
        echo "[setup] gh CLI: installed"
    else
        add_soft_failure "gh CLI" "could not be installed — install via: brew install gh" "$install_output"
    fi
fi

# ── codegraph ────────────────────────────────────────────────────────────────
step "Checking codegraph"
if command -v codegraph >/dev/null 2>&1; then
    echo "[setup] codegraph: OK"
else
    step "Installing codegraph via npm"
    echo "[setup] codegraph: not found — installing via npm..."
    if command -v npm >/dev/null 2>&1; then
        install_output=$(npm install -g @colbymchenry/codegraph 2>&1)
    else
        install_output="npm not available — install Node.js first, then run: npm install -g @colbymchenry/codegraph"
    fi
    if command -v codegraph >/dev/null 2>&1; then
        echo "[setup] codegraph: installed"
    else
        add_failure "codegraph" "could not be installed — run: npm install -g @colbymchenry/codegraph" "$install_output"
    fi
fi

# ── summary ──────────────────────────────────────────────────────────────────
if [ -n "$DETAILS" ]; then
    printf '%s\n' "$DETAILS"
fi

if [ -n "$SOFT_FAILED" ]; then
    printf '\n[setup] Some optional dependencies are missing or could not be installed:%s\n' "$SOFT_FAILED"
fi

if [ -n "$FAILED" ]; then
    printf '\n[setup] Some dependencies are missing or could not be installed:%s\n' "$FAILED" >&2
    exit 1
fi

echo "[setup] All dependencies OK"
