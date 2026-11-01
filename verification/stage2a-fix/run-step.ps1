# Stage 2A 阻塞修复验证运行器
# 用法: pwsh -File run-step.ps1 -LogFile <name> -Command <command string>
param(
    [Parameter(Mandatory = $true)][string]$LogFile,
    [Parameter(Mandatory = $true)][string]$Command
)

$ErrorActionPreference = 'Continue'
$serverDir = 'c:\Zhy\code\go-rag\server'
$outPath = Join-Path $PSScriptRoot $LogFile

Set-Location $serverDir
$sw = [System.Diagnostics.Stopwatch]::StartNew()
$output = & powershell.exe -NoProfile -Command $Command 2>&1 | Out-String
$exit = $LASTEXITCODE
$sw.Stop()

$timestamp = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'
$header = @"
# CMD: $Command
# CWD: $serverDir
# EXIT: $exit
# DATE: $timestamp
# DURATION: $([math]::Round($sw.Elapsed.TotalSeconds,1))s
"@

Set-Content -Path $outPath -Value ($header + "`n" + $output) -Encoding UTF8
Write-Host "LOG: $outPath"
Write-Host "EXIT: $exit"
exit $exit
