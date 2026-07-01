#!/bin/sh
# Paperclip setup script — Linux
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

try_install_pkg() {
    PKG="$1"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get install -y "$PKG" 2>&1
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y "$PKG" 2>&1
    elif command -v yum >/dev/null 2>&1; then
        yum install -y "$PKG" 2>&1
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm "$PKG" 2>&1
    else
        echo "no supported package manager found (apt-get/dnf/yum/pacman)"
        return 1
    fi
}

# ── git ──────────────────────────────────────────────────────────────────────
if command -v git >/dev/null 2>&1; then
    echo "[setup] git: OK"
else
    echo "[setup] git: not found — installing..."
    install_output=$(try_install_pkg git)
    if command -v git >/dev/null 2>&1; then
        echo "[setup] git: installed"
    else
        add_failure "git" "not found and could not be installed — install it manually" "$install_output"
    fi
fi

# ── python3 ──────────────────────────────────────────────────────────────────
if command -v python3 >/dev/null 2>&1; then
    echo "[setup] python3: OK"
else
    echo "[setup] python3: not found — installing..."
    install_output=$(try_install_pkg python3)
    if command -v python3 >/dev/null 2>&1; then
        echo "[setup] python3: installed"
    else
        add_failure "python3" "not found and could not be installed — install it manually" "$install_output"
    fi
fi

# ── pip ──────────────────────────────────────────────────────────────────────
if command -v python3 >/dev/null 2>&1; then
    if ! python3 -m pip --version >/dev/null 2>&1; then
        echo "[setup] pip: not found — trying ensurepip..."
        install_output=$(python3 -m ensurepip --upgrade 2>&1)
        if ! python3 -m pip --version >/dev/null 2>&1; then
            install_output="${install_output}
$(try_install_pkg python3-pip)"
        fi
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
if command -v python3 >/dev/null 2>&1; then
    if python3 -c "from markitdown import MarkItDown" >/dev/null 2>&1; then
        echo "[setup] markitdown: OK"
    else
        echo "[setup] markitdown: not found — installing..."
        install_output=$(python3 -m pip install markitdown 2>&1)
        if ! python3 -c "from markitdown import MarkItDown" >/dev/null 2>&1; then
            # Most modern distros (Debian/Ubuntu 23.04+, and Homebrew's Python)
            # mark their system Python as "externally managed" (PEP 668) and
            # refuse a plain `pip install`. Retry bypassing that guard, but
            # pair it with --user (as Homebrew's own error message recommends)
            # so this installs to the per-user site-packages instead of the
            # system-wide one shared with OS/Homebrew-managed tools — same
            # python3 interpreter can still import it, without risking a
            # shared dependency (e.g. requests, beautifulsoup4) getting
            # upgraded out from under some other tool.
            retry_output=$(python3 -m pip install --break-system-packages --user markitdown 2>&1)
            install_output="${install_output}

--- retry with --break-system-packages --user ---
${retry_output}"
        fi
        if python3 -c "from markitdown import MarkItDown" >/dev/null 2>&1; then
            echo "[setup] markitdown: installed"
        else
            reason="pip install failed — web_fetch markdown conversion will be unavailable"
            pyver=$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])' 2>/dev/null)
            case "$pyver" in
                2.*|3.0|3.1|3.2|3.3|3.4|3.5|3.6|3.7|3.8|3.9)
                    reason="python $pyver is too old — markitdown requires Python >=3.10; install a newer python3 and retry"
                    ;;
            esac
            add_failure "markitdown" "$reason" "$install_output"
        fi
    fi
fi

# ── Node.js / npm ────────────────────────────────────────────────────────────
if command -v npm >/dev/null 2>&1; then
    echo "[setup] npm: OK"
else
    echo "[setup] npm: not found — installing Node.js..."
    install_output=$(try_install_pkg nodejs)
    if command -v npm >/dev/null 2>&1; then
        echo "[setup] npm: installed"
    else
        add_failure "npm" "could not be installed — install Node.js from https://nodejs.org for MCP server npm packages" "$install_output"
    fi
fi

# ── chromium ─────────────────────────────────────────────────────────────────
chromium_ok=0
for p in /opt/pw-browsers/chromium-*/chrome-linux/chrome \
         /usr/bin/chromium /usr/bin/chromium-browser \
         /usr/bin/google-chrome /usr/local/bin/chromium; do
    # expand glob
    for f in $p; do
        if [ -x "$f" ]; then
            chromium_ok=1
            echo "[setup] chromium: OK ($f)"
            break 2
        fi
    done
done
if [ "$chromium_ok" -eq 0 ]; then
    echo "[setup] chromium: not found — installing..."
    install_output=$(try_install_pkg chromium-browser)
    chromium_rc=$?
    if [ "$chromium_rc" -ne 0 ]; then
        more_output=$(try_install_pkg chromium)
        chromium_rc=$?
        install_output="${install_output}
${more_output}"
    fi
    if command -v chromium-browser >/dev/null 2>&1 || command -v chromium >/dev/null 2>&1; then
        echo "[setup] chromium: installed"
    elif [ "$chromium_rc" -eq 0 ]; then
        add_failure "chromium" "install attempted but binary not found — browser_use tool will be unavailable" "$install_output"
    else
        add_failure "chromium" "could not be installed — install chromium-browser manually for browser_use support" "$install_output"
    fi
fi

# ── gh CLI ───────────────────────────────────────────────────────────────────
if command -v gh >/dev/null 2>&1; then
    echo "[setup] gh CLI: OK"
else
    echo "[setup] gh CLI: not found — installing..."
    installed=0
    gh_output=""
    # Try GitHub's official apt repo on Debian/Ubuntu
    if command -v apt-get >/dev/null 2>&1; then
        gh_output=$(
            curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
                  -o /usr/share/keyrings/githubcli-archive-keyring.gpg 2>&1 && \
            echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
                  > /etc/apt/sources.list.d/github-cli.list 2>&1 && \
            apt-get update -qq 2>&1 && \
            apt-get install -y gh 2>&1
        )
        [ $? -eq 0 ] && installed=1
    elif command -v dnf >/dev/null 2>&1; then
        gh_output=$(
            dnf install -y 'dnf-command(config-manager)' 2>&1 && \
            dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo 2>&1 && \
            dnf install -y gh 2>&1
        )
        [ $? -eq 0 ] && installed=1
    else
        gh_output="no supported package manager found (apt-get/dnf) for automatic gh CLI install"
    fi
    if [ "$installed" -eq 1 ] && command -v gh >/dev/null 2>&1; then
        echo "[setup] gh CLI: installed"
    else
        add_soft_failure "gh CLI" "could not be installed — install manually from https://cli.github.com for GitHub MCP server support" "$gh_output"
    fi
fi

# ── codegraph ────────────────────────────────────────────────────────────────
if command -v codegraph >/dev/null 2>&1; then
    echo "[setup] codegraph: OK"
else
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
