#Requires -Version 5
# Paperclip setup script — Windows
# Checks and installs all runtime dependencies.
# Exits 1 with a summary if any dependency could not be installed.

$ErrorActionPreference = 'Continue'
$Failed = @()
$SoftFailed = @()
$Details = @()

# Add-Failure records a blocking dependency failure. $Detail, if given, is the
# raw command output that explains why the install failed — surfaced in the
# UI as an expandable detail alongside the summary reason in $Reason.
function Add-Failure($Name, $Reason, $Detail) {
    Write-Warning "[setup] $Name — $Reason"
    $script:Failed += "  • ${Name}: ${Reason}"
    if ($Detail) {
        $id = $Name -replace ' ', '_'
        $script:Details += "[setup] DETAIL_BEGIN $id`n$Detail`n[setup] DETAIL_END"
    }
}

# Add-SoftFailure is for optional dependencies (currently: gh CLI) whose
# absence should not block the app from starting — only surface a warning.
function Add-SoftFailure($Name, $Reason, $Detail) {
    Write-Host "[setup] SOFT_FAIL: $Name — $Reason"
    $script:SoftFailed += "  • ${Name}: ${Reason}"
    if ($Detail) {
        $id = $Name -replace ' ', '_'
        $script:Details += "[setup] DETAIL_BEGIN $id`n$Detail`n[setup] DETAIL_END"
    }
}

function Test-Command($Cmd) {
    return $null -ne (Get-Command $Cmd -ErrorAction SilentlyContinue)
}

$script:LastWingetOutput = ''

function Invoke-Winget($Id) {
    if (Test-Command winget) {
        $script:LastWingetOutput = winget install --id $Id --silent --accept-package-agreements --accept-source-agreements 2>&1 | Out-String
        return $LASTEXITCODE -eq 0
    }
    $script:LastWingetOutput = 'winget not available on this system'
    return $false
}

# ── git ──────────────────────────────────────────────────────────────────────
if (Test-Command git) {
    Write-Host "[setup] git: OK"
} else {
    Write-Host "[setup] git: not found — installing..."
    $installed = Invoke-Winget 'Git.Git'
    $installOutput = $script:LastWingetOutput
    if ($installed) {
        # Refresh PATH and check again
        $env:PATH = [System.Environment]::GetEnvironmentVariable('PATH', 'Machine') + ';' + [System.Environment]::GetEnvironmentVariable('PATH', 'User')
    }
    if (Test-Command git) {
        Write-Host "[setup] git: installed"
    } elseif ($installed) {
        Add-Failure 'git' 'installed but not yet on PATH — restart the application or add Git to PATH manually' $installOutput
    } else {
        Add-Failure 'git' 'could not be installed — download from https://git-scm.com/download/win' $installOutput
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
    $installed = Invoke-Winget 'Python.Python.3'
    $installOutput = $script:LastWingetOutput
    if ($installed) {
        $env:PATH = [System.Environment]::GetEnvironmentVariable('PATH', 'Machine') + ';' + [System.Environment]::GetEnvironmentVariable('PATH', 'User')
        foreach ($candidate in @('python3', 'python')) {
            if (Test-Command $candidate) { $pythonCmd = (Get-Command $candidate).Source; break }
        }
    }
    if ($pythonCmd) {
        Write-Host "[setup] python3: installed"
    } else {
        Add-Failure 'python3' 'not found — download from https://python.org (check "Add to PATH" during install)' $installOutput
    }
}

# ── pip ──────────────────────────────────────────────────────────────────────
if ($pythonCmd) {
    $pipOk = & $pythonCmd -m pip --version 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[setup] pip: not found — trying ensurepip..."
        $ensureOutput = & $pythonCmd -m ensurepip --upgrade 2>&1 | Out-String
        $pipOk = & $pythonCmd -m pip --version 2>&1
        if ($LASTEXITCODE -ne 0) {
            Add-Failure 'pip' 'not available — run: python -m ensurepip --upgrade' $ensureOutput
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
        $pipOutput = & $pythonCmd -m pip install markitdown 2>&1 | Out-String
        & $pythonCmd -c "from markitdown import MarkItDown" 2>&1 | Out-Null
        if ($LASTEXITCODE -eq 0) {
            Write-Host "[setup] markitdown: installed"
        } else {
            $reason = 'pip install failed — web_fetch markdown conversion will be unavailable'
            $pyver = & $pythonCmd -c 'import sys; print("%d.%d" % sys.version_info[:2])' 2>&1
            if ($pyver -match '^(2\.|3\.[0-9]$)') {
                $reason = "python $pyver is too old — markitdown requires Python >=3.10; install a newer python3 and retry"
            }
            Add-Failure 'markitdown' $reason $pipOutput
        }
    }
}

# ── Node.js / npm ────────────────────────────────────────────────────────────
if (Test-Command npm) {
    Write-Host "[setup] npm: OK"
} else {
    Write-Host "[setup] npm: not found — installing Node.js..."
    $installed = Invoke-Winget 'OpenJS.NodeJS'
    $installOutput = $script:LastWingetOutput
    if ($installed) {
        $env:PATH = [System.Environment]::GetEnvironmentVariable('PATH', 'Machine') + ';' + [System.Environment]::GetEnvironmentVariable('PATH', 'User')
    }
    if (Test-Command npm) {
        Write-Host "[setup] npm: installed"
    } elseif ($installed) {
        Add-Failure 'npm' 'installed but not yet on PATH — restart or install Node.js from https://nodejs.org' $installOutput
    } else {
        Add-Failure 'npm' 'could not be installed — download Node.js from https://nodejs.org for MCP server npm packages' $installOutput
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
    $installOutput = $script:LastWingetOutput
    if (-not $installed) {
        $installed = Invoke-Winget 'Google.Chrome'
        $installOutput = "$installOutput`n$($script:LastWingetOutput)"
    }
    if ($installed) {
        Write-Host "[setup] chromium: installed (restart may be required to locate binary)"
    } else {
        Add-Failure 'chromium' 'could not be installed — download from https://www.chromium.org for browser_use support' $installOutput
    }
}

# ── gh CLI ───────────────────────────────────────────────────────────────────
if (Test-Command gh) {
    Write-Host "[setup] gh CLI: OK"
} else {
    Write-Host "[setup] gh CLI: not found — installing..."
    $installed = Invoke-Winget 'GitHub.cli'
    $installOutput = $script:LastWingetOutput
    if ($installed) {
        $env:PATH = [System.Environment]::GetEnvironmentVariable('PATH', 'Machine') + ';' + [System.Environment]::GetEnvironmentVariable('PATH', 'User')
    }
    if (Test-Command gh) {
        Write-Host "[setup] gh CLI: installed"
    } elseif ($installed) {
        Add-SoftFailure 'gh CLI' 'installed but not yet on PATH — restart or run: winget install GitHub.cli' $installOutput
    } else {
        Add-SoftFailure 'gh CLI' 'could not be installed — download from https://cli.github.com' $installOutput
    }
}

# ── codegraph ────────────────────────────────────────────────────────────────
if (Test-Command codegraph) {
    Write-Host "[setup] codegraph: OK"
} else {
    Write-Host "[setup] codegraph: not found — installing via npm..."
    if (Test-Command npm) {
        $installOutput = & npm install -g @colbymchenry/codegraph 2>&1 | Out-String
        if ($LASTEXITCODE -eq 0 -and (Test-Command codegraph)) {
            Write-Host "[setup] codegraph: installed"
        } else {
            Add-Failure 'codegraph' 'could not be installed — run: npm install -g @colbymchenry/codegraph' $installOutput
        }
    } else {
        Add-Failure 'codegraph' 'npm not available — install Node.js first, then run: npm install -g @colbymchenry/codegraph' ''
    }
}

# ── summary ──────────────────────────────────────────────────────────────────
if ($Details.Count -gt 0) {
    Write-Host ($Details -join "`n")
}

if ($SoftFailed.Count -gt 0) {
    $softList = $SoftFailed -join "`n"
    Write-Host "[setup] Some optional dependencies are missing or could not be installed:`n$softList"
}

if ($Failed.Count -gt 0) {
    $list = $Failed -join "`n"
    Write-Error "[setup] Some dependencies are missing or could not be installed:`n$list"
    exit 1
}

Write-Host "[setup] All dependencies OK"
