# =============================================================================
# _env-setup.ps1 — go-rag verification script path configuration
# =============================================================================
#
# SYNOPSIS
#   Dot-source this file before running any verification/*.ps1 script to
#   supply real filesystem paths.  Without it, scripts fall back to the
#   desensitized placeholder defaults (<WORKSPACE>, <TOOLCHAIN>, <TEMP>)
#   which are NOT valid paths and will fail at runtime.
#
# USAGE
#   # Option A: set environment variables in your shell session, then run scripts
#   $env:RAG_WORKSPACE = 'C:\path\to\go-rag'
#   $env:RAG_TOOLCHAIN = 'C:\path\to\msys64'
#   $env:RAG_TEMP     = 'C:\path\to\temp'
#   .\verification\stage1d1\run-all.ps1
#
#   # Option B: dot-source this file after editing the values below
#   . .\verification\_env-setup.ps1
#   .\verification\stage1d1\run-all.ps1
#
# ENVIRONMENT VARIABLES
#   RAG_WORKSPACE  — repository root (contains server\, verification\, etc.)
#   RAG_TOOLCHAIN  — MSYS2/UCRT64 toolchain root (parent of ucrt64\bin\)
#   RAG_TEMP       — dedicated temp directory for go test binaries
#
# QUICK TEST (syntax check only — does NOT execute any script)
#   $errors = $null
#   Get-ChildItem -Recurse -Filter '*.ps1' verification\ | ForEach-Object {
#       [System.Management.Automation.Language.Parser]::ParseFile(
#           $_.FullName, [ref]$null, [ref]$errors) | Out-Null
#       if ($errors.Count -gt 0) { Write-Host "FAIL: $($_.FullName)" -ForegroundColor Red; $errors | ForEach-Object { Write-Host "  $_" } }
#       else { Write-Host "OK:   $($_.FullName)" -ForegroundColor Green }
#   }
# =============================================================================

# ---- Edit these values to match your local environment ----
# Then dot-source: . .\verification\_env-setup.ps1

if (-not $env:RAG_WORKSPACE) {
    # $env:RAG_WORKSPACE = 'C:\your\path\to\go-rag'
    Write-Verbose 'RAG_WORKSPACE not set — scripts will use placeholder default'
}

if (-not $env:RAG_TOOLCHAIN) {
    # $env:RAG_TOOLCHAIN = 'C:\your\path\to\msys64'
    Write-Verbose 'RAG_TOOLCHAIN not set — scripts will use placeholder default'
}

if (-not $env:RAG_TEMP) {
    # $env:RAG_TEMP = 'C:\your\temp\path'
    Write-Verbose 'RAG_TEMP not set — scripts will use placeholder default'
}

# ---- Common configuration snippet (can be embedded in individual scripts) ----
# The block below shows what each verification script includes at its top:
#
#   $Workspace = if ($env:RAG_WORKSPACE) { $env:RAG_WORKSPACE } else { '<WORKSPACE>' }
#   $Toolchain = if ($env:RAG_TOOLCHAIN) { $env:RAG_TOOLCHAIN } else { '<TOOLCHAIN>' }
#   $TempDir   = if ($env:RAG_TEMP)     { $env:RAG_TEMP }     else { '<TEMP>' }

Write-Verbose 'Environment configuration loaded.'
