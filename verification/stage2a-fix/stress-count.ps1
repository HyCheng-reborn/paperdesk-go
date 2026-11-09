# 压测日志逐用例统计
# 用法: powershell -NoProfile -File stress-count.ps1 -LogFile <name>
param(
    [Parameter(Mandatory = $true)][string]$LogFile
)

$path = Join-Path $PSScriptRoot $LogFile
$lines = Get-Content $path
$top = @{}
foreach ($ln in $lines) {
    if ($ln -match '^=== RUN\s+(Test\w+)$') {
        $name = $Matches[1]
        if ($top.ContainsKey($name)) { $top[$name]++ } else { $top[$name] = 1 }
    }
}
Write-Host "distinct top-level tests: $($top.Count)"
$top.GetEnumerator() | Sort-Object Name | ForEach-Object { Write-Host ("{0,3} x {1}" -f $_.Value, $_.Key) }
$fail = ($lines | Select-String -Pattern '^\s*--- FAIL|^FAIL\b|^panic:' ).Count
Write-Host "FAIL/panic lines: $fail"
$lines | Select-String '^ok\s' | ForEach-Object { Write-Host $_.Line }
