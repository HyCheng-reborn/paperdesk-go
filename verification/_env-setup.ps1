# _env-setup.ps1 — Shared environment initialization for verification scripts
# Dot-source this file at the top of each verification script:
#   . "$PSScriptRoot\..\_env-setup.ps1"   # adjust relative path as needed
#
# After dot-sourcing, the following functions are available:
#   Test-RequiredEnv   - Fail-fast validation of required environment variables
#   Set-ToolchainEnv   - Configure MSYS2/UCRT64 toolchain environment
#   Assert-ToolchainExists - Validate that the toolchain gcc exists

function Test-RequiredEnv {
    <#
    .SYNOPSIS
        Validates that required environment variables are set, non-empty,
        and not still containing desensitization placeholders.
    .PARAMETER Name
        The environment variable name (e.g., 'RAG_WORKSPACE').
    .PARAMETER Placeholder
        The placeholder pattern to detect (e.g., '<WORKSPACE>').
    .OUTPUTS
        Returns the resolved value (from env or placeholder).
    .NOTES
        Exits with code 1 if validation fails.
    #>
    param(
        [Parameter(Mandatory)][string]$Name,
        [string]$Placeholder = ''
    )

    $value = [System.Environment]::GetEnvironmentVariable($Name)

    if (-not $value -or $value -eq '') {
        Write-Error "Required environment variable $Name is not set or empty. Set it before running this script."
        exit 1
    }

    if ($Placeholder -and $value -eq $Placeholder) {
        Write-Error "Environment variable $Name still contains the desensitization placeholder '$Placeholder'. Set it to the actual path."
        exit 1
    }

    # Check for angle-bracket patterns (generic placeholder detection)
    if ($value -match '<[^>]+>') {
        Write-Error "Environment variable $Name appears to contain an unresolved placeholder: '$value'."
        exit 1
    }

    return $value
}

function Set-ToolchainEnv {
    <#
    .SYNOPSIS
        Configures MSYS2/UCRT64 toolchain environment variables.
    .PARAMETER ToolchainPath
        Path to the toolchain root (e.g., C:\msys64).
    .PARAMETER IncludeCXX
        Whether to also set CXX for C++ compiler.
    #>
    param(
        [Parameter(Mandatory)][string]$ToolchainPath,
        [switch]$IncludeCXX
    )

    $ucrtBin = Join-Path $ToolchainPath 'ucrt64\bin'

    $env:CGO_ENABLED = '1'
    $env:GOPROXY = 'off'
    $env:GOSUMDB = 'off'
    $env:GOFLAGS = ''
    $env:CC = Join-Path $ucrtBin 'gcc.exe'

    if ($IncludeCXX) {
        $env:CXX = Join-Path $ucrtBin 'g++.exe'
    }

    # Prepend to PATH
    $currentPath = $env:PATH -split ';' | Where-Object { $_ -ne $ucrtBin }
    $env:PATH = ($ucrtBin + ';' + ($currentPath -join ';'))
}

function Assert-ToolchainExists {
    <#
    .SYNOPSIS
        Validates that the toolchain gcc exists.
    .PARAMETER ToolchainPath
        Path to the toolchain root.
    #>
    param([Parameter(Mandatory)][string]$ToolchainPath)

    $gccPath = Join-Path $ToolchainPath 'ucrt64\bin\gcc.exe'
    if (-not (Test-Path $gccPath)) {
        Write-Error "Toolchain gcc not found at expected path. Set RAG_TOOLCHAIN correctly."
        exit 1
    }
}
