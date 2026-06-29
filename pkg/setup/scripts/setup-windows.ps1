#Requires -Version 5
$ErrorActionPreference = 'Stop'

# ── python3 ──────────────────────────────────────────────────────────────────
$python = $null
foreach ($candidate in @('python3', 'python', 'py')) {
    $found = Get-Command $candidate -ErrorAction SilentlyContinue
    if ($found) { $python = $found.Source; break }
}

if (-not $python) {
    Write-Error "[setup] ERROR: python3 not found. Please install Python 3 from https://python.org and ensure it is added to PATH."
    exit 1
}

Write-Host "[setup] Using Python: $python"

# ── pip ──────────────────────────────────────────────────────────────────────
$pipOk = & $python -m pip --version 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Host "[setup] pip not found — attempting ensurepip..."
    & $python -m ensurepip --upgrade
    if ($LASTEXITCODE -ne 0) {
        Write-Error "[setup] ERROR: pip not available after ensurepip"
        exit 1
    }
}

# ── markitdown ───────────────────────────────────────────────────────────────
& $python -c "from markitdown import MarkItDown" 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Host "[setup] markitdown not found — installing..."
    & $python -m pip install --quiet markitdown
    if ($LASTEXITCODE -ne 0) {
        Write-Error "[setup] ERROR: failed to install markitdown"
        exit 1
    }
    & $python -c "from markitdown import MarkItDown" 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Error "[setup] ERROR: markitdown install reported success but import still fails"
        exit 1
    }
}

Write-Host "[setup] All dependencies OK"
