# Helper: run a verification command and save log with required header.
# Uses native invocation so $LASTEXITCODE is reliable (Start-Process exit code is unavailable in this sandbox).
param(
    [Parameter(Mandatory = $true)][string]$LogFile,
    [Parameter(Mandatory = $true)][string]$CmdLine,
    [Parameter(Mandatory = $true)][string]$EnvNote,
    [bool]$UseMsys = $false,
    [Parameter(Mandatory = $true)][string]$Exe,
    [Parameter(Mandatory = $true)][string[]]$CmdArgs
)

$ErrorActionPreference = 'Continue'
Set-Location 'c:\Zhy\code\go-rag\server'

if ($UseMsys) {
    $env:PATH = "C:\msys64\ucrt64\bin;$env:PATH"
    $env:CC = 'gcc'
    $env:CGO_ENABLED = '1'
}

$sw = [System.Diagnostics.Stopwatch]::StartNew()
# Direct array invocation: arguments are passed verbatim, so shell metacharacters (|, ", spaces) are safe.
& $Exe @CmdArgs *>&1 | Set-Content -Path "$LogFile.body.tmp" -Encoding UTF8
$ec = $LASTEXITCODE
$sw.Stop()
if ($null -eq $ec) { $ec = -1 }

$body = ''
if (Test-Path "$LogFile.body.tmp") {
    $body = Get-Content "$LogFile.body.tmp" -Raw -ErrorAction SilentlyContinue
    Remove-Item "$LogFile.body.tmp" -Force -ErrorAction SilentlyContinue
}
if ($null -eq $body) { $body = '' }

$d = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'
$gccVer = ''
if ($UseMsys) {
    $gccVer = (& C:\msys64\ucrt64\bin\gcc.exe --version 2>&1 | Select-Object -First 1) -join ''
}
$header = "# CMD: $CmdLine`r`n# CWD: c:\Zhy\code\go-rag\server`r`n# ENV: $EnvNote`r`n# EXIT: $ec`r`n# DATE: $d`r`n# DURATION_SEC: $([math]::Round($sw.Elapsed.TotalSeconds, 1))`r`n"
if ($gccVer) { $header += "# GCC: $gccVer`r`n" }
$header += "# GIT_HEAD: $((git rev-parse HEAD 2>&1) -join '')`r`n# NOTE: stdout and stderr are interleaved in a single stream`r`n"
Set-Content -Path $LogFile -Value ($header + "--- OUTPUT BEGIN ---`r`n" + $body + "--- OUTPUT END ---`r`n") -NoNewline -Encoding UTF8

Write-Output "EXITCODE=$ec"
Write-Output "DURATION_SEC=$([math]::Round($sw.Elapsed.TotalSeconds, 1))"
Write-Output "LOG=$LogFile"
Write-Output "LOG_LINES=$((Get-Content $LogFile | Measure-Object -Line).Lines)"
