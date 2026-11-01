# 日志摘要分析器
# 用法: powershell -NoProfile -File summarize.ps1 -LogFile <name>
param(
    [Parameter(Mandatory = $true)][string]$LogFile
)

$path = Join-Path $PSScriptRoot $LogFile
if (-not (Test-Path $path)) { Write-Host "MISSING: $path"; exit 1 }
$c = Get-Content $path

$runAll   = ($c | Select-String '=== RUN').Count
$runTop   = ($c | Select-String '^=== RUN\s+Test').Count
$passAll  = ($c | Select-String '--- PASS').Count
$passTop  = ($c | Select-String '^--- PASS').Count
$failAll  = ($c | Select-String '--- FAIL').Count
$failPkg  = ($c | Select-String '^FAIL').Count
$okPkg    = ($c | Select-String '^ok\s').Count
$panics   = ($c | Select-String 'panic:').Count
$dataRace = ($c | Select-String 'DATA RACE').Count
$skipAll  = ($c | Select-String '--- SKIP').Count

Write-Host "FILE        : $LogFile"
Write-Host "RUN total   : $runAll (top-level: $runTop)"
Write-Host "PASS total  : $passAll (top-level: $passTop)"
Write-Host "FAIL total  : $failAll (pkg-level FAIL lines: $failPkg)"
Write-Host "SKIP total  : $skipAll"
Write-Host "ok pkg lines: $okPkg"
Write-Host "panic lines : $panics"
Write-Host "DATA RACE   : $dataRace"
Write-Host "--- header ---"
$c | Select-Object -First 5 | ForEach-Object { Write-Host $_ }
Write-Host "--- ok/FAIL/pkg summary lines ---"
$c | Select-String '^(ok|FAIL|---)\s|^(ok|FAIL)\s' | ForEach-Object { Write-Host $_.Line }
Write-Host "--- FAIL/panic detail (first 30) ---"
$c | Select-String 'FAIL|panic|DATA RACE|exit status' | Select-Object -First 30 | ForEach-Object { Write-Host $_.Line }
