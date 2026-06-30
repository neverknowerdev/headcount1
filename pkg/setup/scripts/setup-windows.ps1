#Requires -Version 5
# Paperclip setup script — Windows
# Checks and installs all runtime dependencies.
# Exits 1 with a summary if any dependency could not be installed.

$ErrorActionPreference = 'Continue'
$Failed = @()

function Add-Failure($Name, $Reason) {
    Write-Warning "[setup] $Name — $Reason"
    $script:Failed += "  • ${Name}: ${Reason}"
}

function Test-Command($Cmd) {
    return $null -ne (Get-Command $Cmd -ErrorAction SilentlyContinue)
}

function Invoke-Winget($Id) {
    if (Test-Command winget) {
        winget install --id $Id --silent --accept-package-agreements --accept-source-agreements 2>&1 | Out-Null
        return $LASTEXITCODE -eq 0
    }
    return $false
}

# ── git ──────────────────────────────────────────────────────────────────────
if (Test-Command git) {
    Write-Host "[setup] git: OK"
} else {
    Write-Host "[setup] git: not found — installing..."
    if (Invoke-Winget 'Git.Git') {
        # Refresh PATH and check again
        $env:PATH = [System.Environment]::GetEnvironmentVariable('PATH', 'Machine') + ';' + [System.Environment]::GetEnvironmentVariable('PATH', 'User')
        if (Test-Command git) {
            Write-Host "[setup] git: installed"
        } else {
            Add-Failure 'git' 'installed but not yet on PATH — restart the application or add Git to PATH manually'
        }
    } else {
        Add-Failure 'git' 'could not be installed — download from https://git-scm.com/download/win'
    }
}

# ── python3 ──────────────────────────────────────────────────────────────────
$pythonCmd = $null
foreach ($candidate in @('python3', 'python', 'py')) {
    if (Test-Command $candidate) {
        # Confirm it is actually Python 3 (not a Store stub)
        $ver = & $candidate --version 2>&1
        if ($ver -match 'Python 3') {
            $pythonCmd = (Get-Command $candidate).Source
            break
        }
    }
}

if ($pythonCmd) {
    Write-Host "[setup] python3: OK ($pythonCmd)"
} else {
    Write-Host "[setup] python3: not found — installing..."
    if (Invoke-Winget 'Python.Python.3') {
        $env:PATH = [System.Environment]::GetEnvironmentVariable('PATH', 'Machine') + ';' + [System.Environment]::GetEnvironmentVariable('PATH', 'User')
        foreach ($candidate in @('python3', 'python')) {
            if (Test-Command $candidate) { $pythonCmd = (Get-Command $candidate).Source; break }
        }
    }
    if ($pythonCmd) {
        Write-Host "[setup] python3: installed"
    } else {
        Add-Failure 'python3' 'not found — download from https://python.org (check "Add to PATH" during install)'
    }
}

# ── pip ──────────────────────────────────────────────────────────────────────
if ($pythonCmd) {
    $pipOk = & $pythonCmd -m pip --version 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[setup] pip: not found — trying ensurepip..."
        & $pythonCmd -m ensurepip --upgrade 2>&1 | Out-Null
        $pipOk = & $pythonCmd -m pip --version 2>&1
        if ($LASTEXITCODE -ne 0) {
            Add-Failure 'pip' 'not available — run: python -m ensurepip --upgrade'
        } else {
            Write-Host "[setup] pip: installed"
        }
    } else {
        Write-Host "[setup] pip: OK"
    }
}

# ── markitdown ───────────────────────────────────────────────────────────────
if ($pythonCmd) {
    & $pythonCmd -c "from markitdown import MarkItDown" 2>&1 | Out-Null
    if ($LASTEXITCODE -eq 0) {
        Write-Host "[setup] markitdown: OK"
    } else {
        Write-Host "[setup] markitdown: not found — installing..."
        & $pythonCmd -m pip install --quiet markitdown 2>&1 | Out-Null
        & $pythonCmd -c "from markitdown import MarkItDown" 2>&1 | Out-Null
        if ($LASTEXITCODE -eq 0) {
            Write-Host "[setup] markitdown: installed"
        } else {
            Add-Failure 'markitdown' 'pip install failed — web_fetch markdown conversion will be unavailable'
        }
    }
}

# ── Node.js / npm ────────────────────────────────────────────────────────────
if (Test-Command npm) {
    Write-Host "[setup] npm: OK"
} else {
    Write-Host "[setup] npm: not found — installing Node.js..."
    if (Invoke-Winget 'OpenJS.NodeJS') {
        $env:PATH = [System.Environment]::GetEnvironmentVariable('PATH', 'Machine') + ';' + [System.Environment]::GetEnvironmentVariable('PATH', 'User')
        if (Test-Command npm) {
            Write-Host "[setup] npm: installed"
        } else {
            Add-Failure 'npm' 'installed but not yet on PATH — restart or install Node.js from https://nodejs.org'
        }
    } else {
        Add-Failure 'npm' 'could not be installed — download Node.js from https://nodejs.org for MCP server npm packages'
    }
}

# ── chromium ─────────────────────────────────────────────────────────────────
$chromiumPaths = @(
    "$env:LOCALAPPDATA\Chromium\Application\chrome.exe",
    "$env:PROGRAMFILES\Chromium\Application\chrome.exe",
    "$env:PROGRAMFILES\Google\Chrome\Application\chrome.exe",
    "${env:PROGRAMFILES(X86)}\Google\Chrome\Application\chrome.exe"
)
$chromiumFound = $chromiumPaths | Where-Object { Test-Path $_ } | Select-Object -First 1
if ($chromiumFound) {
    Write-Host "[setup] chromium: OK ($chromiumFound)"
} elseif (Test-Command chrome) {
    Write-Host "[setup] chromium: OK ($(Get-Command chrome | Select-Object -ExpandProperty Source))"
} else {
    Write-Host "[setup] chromium: not found — installing..."
    $installed = Invoke-Winget 'Chromium.Chromium'
    if (-not $installed) { $installed = Invoke-Winget 'Google.Chrome' }
    if ($installed) {
        Write-Host "[setup] chromium: installed (restart may be required to locate binary)"
    } else {
        Add-Failure 'chromium' 'could not be installed — download from https://www.chromium.org for browser_use support'
    }
}

# ── gh CLI ───────────────────────────────────────────────────────────────────
if (Test-Command gh) {
    Write-Host "[setup] gh CLI: OK"
} else {
    Write-Host "[setup] gh CLI: not found — installing..."
    if (Invoke-Winget 'GitHub.cli') {
        $env:PATH = [System.Environment]::GetEnvironmentVariable('PATH', 'Machine') + ';' + [System.Environment]::GetEnvironmentVariable('PATH', 'User')
        if (Test-Command gh) {
            Write-Host "[setup] gh CLI: installed"
        } else {
            Add-Failure 'gh CLI' 'installed but not yet on PATH — restart or run: winget install GitHub.cli'
        }
    } else {
        Add-Failure 'gh CLI' 'could not be installed — download from https://cli.github.com'
    }
}

# ── codegraph ────────────────────────────────────────────────────────────────
if (Test-Command codegraph) {
    Write-Host "[setup] codegraph: OK"
} else {
    Write-Host "[setup] codegraph: not found — installing via npm..."
    if (Test-Command npm) {
        & npm install -g @colbymchenry/codegraph 2>&1 | Out-Null
        if ($LASTEXITCODE -eq 0 -and (Test-Command codegraph)) {
            Write-Host "[setup] codegraph: installed"
        } else {
            Add-Failure 'codegraph' 'could not be installed — run: npm install -g @colbymchenry/codegraph'
        }
    } else {
        Add-Failure 'codegraph' 'npm not available — install Node.js first, then run: npm install -g @colbymchenry/codegraph'
    }
}

# ── summary ──────────────────────────────────────────────────────────────────
if ($Failed.Count -gt 0) {
    $list = $Failed -join "`n"
    Write-Error "[setup] Some dependencies are missing or could not be installed:`n$list"
    exit 1
}

Write-Host "[setup] All dependencies OK"
