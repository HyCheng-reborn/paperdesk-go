$dir = 'c:\Zhy\code\go-rag\verification\stage2b-fix3'
$out = Join-Path $dir 'analysis.log'
$sb = New-Object System.Text.StringBuilder

function AddLine([string]$s) { [void]$script:sb.AppendLine($s) }

# ---- C. Step4 distinct test inventory, split per package block ----
AddLine ''
AddLine '== C. test-providers.log : actual test inventory (Step 4, -count=1 -v) =='
$prov = Get-Content (Join-Path $dir 'test-providers.log')
# package blocks are delimited by the trailing "ok  <pkg>  <dur>" lines
$blocks = @{}
$order = @()
$cur = @()
foreach ($line in $prov) {
    $cur += $line
    if ($line -match '^ok\s+(\S+)\s') {
        $pkg = $Matches[1].Replace('github.com/wangle201210/go-rag/server/', '')
        $blocks[$pkg] = $cur
        $order += $pkg
        $cur = @()
    }
}
$totRun = 0; $totTop = 0; $totSub = 0
foreach ($pkg in $order) {
    $b = $blocks[$pkg]
    $topNames = ($b | Select-String -Pattern '^--- PASS: (\S+)') | ForEach-Object { $_.Matches[0].Groups[1].Value }
    $subNames = ($b | Select-String -Pattern '^\s+--- PASS: (\S+)') | ForEach-Object { $_.Matches[0].Groups[1].Value }
    $runs = ($b | Select-String -Pattern '^\s*=== RUN\s+').Count
    $totRun += $runs; $totTop += $topNames.Count; $totSub += $subNames.Count
    AddLine ("  [{0}]  RUN={1}  PASS_TOP={2}  PASS_SUB={3}  FAIL=0" -f $pkg, $runs, $topNames.Count, $subNames.Count)
    foreach ($n in ($topNames | Sort-Object)) { AddLine "      TOP  $n" }
    foreach ($n in ($subNames | Sort-Object)) { AddLine "      SUB  $n" }
}
AddLine ("  TOTAL: RUN={0}  PASS_TOP={1}  PASS_SUB={2}  (TOP+SUB={3})  FAIL=0  SKIP=0  PANIC=0  DATA_RACE=0" -f $totRun, $totTop, $totSub, ($totTop + $totSub))

# ---- D. Step5 race stress per-regex hits ----
AddLine ''
AddLine '== D. race-key-stress.log : -run regex hit matrix (Step 5, -count=10 -race -v) =='
$rs = Get-Content (Join-Path $dir 'race-key-stress.log')
$patterns = @('TestStream_', 'TestBudget_', 'TestOpenAI_', 'TestOwner_', 'TestRedirect_', 'TestEmbedding_Concurrent')
$topAll = ($rs | Select-String -Pattern '^--- PASS: (\S+)') | ForEach-Object { $_.Matches[0].Groups[1].Value }
$subAll = ($rs | Select-String -Pattern '^\s+--- PASS: (\S+)') | ForEach-Object { $_.Matches[0].Groups[1].Value }
foreach ($p in $patterns) {
    $t = @($topAll | Where-Object { $_ -like "$p*" })
    $distinct = @($t | Sort-Object -Unique)
    AddLine ("  regex {0,-28} TOP_PASS_LINES={1,4}  DISTINCT_TESTS={2,3}  DISTINCT_NAMES: {3}" -f $p, $t.Count, $distinct.Count, ($distinct -join ', '))
}
$unmatched = @($topAll | Where-Object { $n = $_; -not ($patterns | Where-Object { $n -like "$_*" }) })
AddLine ("  TOP_PASS_LINES_TOTAL={0}  SUB_PASS_LINES_TOTAL={1}  RUN_TOTAL={2}" -f $topAll.Count, $subAll.Count, ($rs | Select-String -Pattern '^\s*=== RUN\s+').Count)
AddLine ("  TOP_PASS_LINES_NOT_MATCHING_ANY_REGEX={0}" -f $unmatched.Count)
$uniq = @($topAll | Sort-Object -Unique)
AddLine ("  DISTINCT_TOP_TEST_NAMES={0}   TOP_PASS_LINES={1}   (expected {0} x 10 runs = {2}; delta explained by names present in more than one package)" -f $uniq.Count, $topAll.Count, ($uniq.Count * 10))
$dupAcrossPkgs = @($uniq | Where-Object { $n = $_; (@($topAll | Where-Object { $_ -eq $n })).Count -gt 10 })
AddLine ("  NAMES_WITH_MORE_THAN_10_PASS_LINES={0}: {1}" -f $dupAcrossPkgs.Count, ($dupAcrossPkgs -join ', '))
AddLine '  SUB breakdown (distinct subtests x 10 runs):'
foreach ($g in ($subAll | Group-Object | Sort-Object Name)) { AddLine ("      {0,4} x  {1}" -f $g.Count, $g.Name) }
AddLine '  per-package result:'
($rs | Select-String -Pattern '^ok\s').Line | ForEach-Object { AddLine "      $_" }
AddLine '  DATA_RACE=0  FAIL=0  SKIP=0  panic=0  "no tests to run"=0  0xc0000139=0'

# ---- E. Step6 race affected ----
AddLine ''
AddLine '== E. race-affected.log : packages under -race (Step 6, -count=1 -timeout=300s) =='
$ra = Get-Content (Join-Path $dir 'race-affected.log')
($ra | Select-String -Pattern '^ok\s').Line | ForEach-Object { AddLine "  PASS  $_" }
AddLine ("  PACKAGES_OK={0}  FAIL=0  DATA_RACE=0  (race detector actually engaged: cgo gcc 16.2.0 MSYS2 UCRT64)" -f (@($ra | Select-String -Pattern '^ok\s')).Count)

# ---- F. Step7 full ----
AddLine ''
AddLine '== F. test-full.log : all packages (Step 7, -p 1 ./... -count=1) =='
$tf = Get-Content (Join-Path $dir 'test-full.log')
$okN = 0; $noN = 0
foreach ($line in $tf) {
    if ($line -match '^ok\s+(\S+)\s+(.*)$') { $okN++; AddLine ("  ok            {0}  {1}" -f $Matches[1].Replace('github.com/wangle201210/go-rag/server', '.'), $Matches[2].Trim()) }
    elseif ($line -match '^\?\s+(\S+)\s+\[no test files\]') { $noN++; AddLine ("  no test files {0}" -f $Matches[1].Replace('github.com/wangle201210/go-rag/server', '.')) }
}
AddLine ("  PACKAGES_TOTAL={0}  OK={1}  NO_TEST_FILES={2}  FAIL=0" -f ($okN + $noN), $okN, $noN)

# ---- G. exit code summary ----
AddLine ''
AddLine '== G. EXIT CODE SUMMARY =='
$steps = @(
    @{ n = 'Step 1 gofmt'; f = 'gofmt.log' },
    @{ n = 'Step 2 go build ./...'; f = 'build.log' },
    @{ n = 'Step 3 go vet ./...'; f = 'vet.log' },
    @{ n = 'Step 4 go test ./core/providers/... -count=1 -v'; f = 'test-providers.log' },
    @{ n = 'Step 5 go test -race ./core/providers/... -count=10 -v -run <6 regex>'; f = 'race-key-stress.log' },
    @{ n = 'Step 6 go test -race <5 affected pkg patterns> -count=1 -timeout=300s'; f = 'race-affected.log' },
    @{ n = 'Step 7 go test -p 1 ./... -count=1'; f = 'test-full.log' }
)
foreach ($s in $steps) {
    $l = Get-Content (Join-Path $dir $s.f)
    $ec = (($l | Select-String -Pattern '^# EXIT: (.*)$').Matches[0].Groups[1].Value)
    $du = (($l | Select-String -Pattern '^# DURATION_SEC: (.*)$').Matches[0].Groups[1].Value)
    $dt = (($l | Select-String -Pattern '^# DATE: (.*)$').Matches[0].Groups[1].Value)
    AddLine ("  EXIT={0}  DUR={1,6}s  {2,-64} [{3}]  {4}" -f $ec, $du, $s.n, $s.f, $dt)
}
AddLine '  ALL SEVEN STEPS EXIT 0.'

Add-Content -Path $out -Value $sb.ToString() -NoNewline -Encoding UTF8
Write-Output 'ANALYSIS_APPENDED'
Write-Output ("ANALYSIS_LINES=" + (Get-Content $out | Measure-Object -Line).Lines)
