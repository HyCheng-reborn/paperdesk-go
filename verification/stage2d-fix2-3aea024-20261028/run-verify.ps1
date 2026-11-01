$ErrorActionPreference = 'Continue'
Set-Location 'C:\Zhy\code\go-rag\server'
$dir = 'C:\Zhy\code\go-rag\verification\stage2d-fix2-3aea024-20261028'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$utf8 = New-Object System.Text.UTF8Encoding($false)

function Save($name, $lines) {
    $text = (($lines | ForEach-Object { "$_" }) -join "`r`n")
    [System.IO.File]::WriteAllText((Join-Path $dir $name), $text, $utf8)
}

$results = New-Object System.Collections.ArrayList

# ---------- environment ----------
$envLines = New-Object System.Collections.ArrayList
$envLines += "Stage 2D S-1 fix batch 2 (incremental review of 3aea024) - environment"
$envLines += ""
$envLines += "repo            = C:/Zhy/code/go-rag"
$envLines += "work dir        = C:/Zhy/code/go-rag/server (unless noted)"
$envLines += "go version      = $(& go version 2>&1)"
$envLines += "GOOS/GOARCH     = $(& go env GOOS)/$(& go env GOARCH)"
$envLines += "CGO_ENABLED     = $(& go env CGO_ENABLED)"
$envLines += "gcc (UCRT64)    = $(& 'C:\msys64\ucrt64\bin\gcc.exe' --version 2>&1 | Select-Object -First 1)"
$envLines += "fixed start     = $(& git rev-parse HEAD 2>&1)"
$envLines += "parent          = $(& git rev-parse HEAD^ 2>&1)"
$envLines += "branch          = $(& git branch --show-current 2>&1)"
$envLines += ""
$envLines += "git status --porcelain (before this batch's commit, evidence dir excluded):"
$envLines += (& git status --porcelain 2>&1 | Where-Object { $_ -notmatch 'stage2d-fix2' })
Save 'environment.log' $envLines

# ---------- [1] default unit tests ----------
$out = & go test -p 1 ./core/probe/... -count=1 -v 2>&1
$code = $LASTEXITCODE
Save 'test-probe-default.log' $out
$pass = ($out | Where-Object { $_ -match '^--- PASS' }).Count
$fails = ($out | Where-Object { $_ -match '^--- FAIL' }).Count
$results += "[1] go test -p 1 ./core/probe/... -count=1 -v"
$results += "    exit $code ; --- PASS count=$pass ; --- FAIL count=$fails ; log: test-probe-default.log"
$results += ""

# ---------- [2] online unit tests ----------
$out = & go test -tags online -p 1 ./core/probe/... -count=1 -v 2>&1
$code = $LASTEXITCODE
Save 'test-probe-online.log' $out
$pass = ($out | Where-Object { $_ -match '^--- PASS' }).Count
$fails = ($out | Where-Object { $_ -match '^--- FAIL' }).Count
$results += "[2] go test -tags online -p 1 ./core/probe/... -count=1 -v"
$results += "    exit $code ; --- PASS count=$pass ; --- FAIL count=$fails ; log: test-probe-online.log"
$results += ""

# ---------- race env (UCRT64) ----------
$env:PATH = "C:\msys64\ucrt64\bin;$env:PATH"
$env:CC = 'C:/msys64/ucrt64/bin/gcc.exe'

# ---------- [3] race default ----------
$out = & go test -race -p 1 ./core/probe/... -count=1 -v 2>&1
$code = $LASTEXITCODE
Save 'race-probe-default.log' $out
$races = ($out | Where-Object { $_ -match 'DATA RACE' }).Count
$results += "[3] PATH+=C:/msys64/ucrt64/bin ; CC=C:/msys64/ucrt64/bin/gcc.exe"
$results += "    go test -race -p 1 ./core/probe/... -count=1 -v"
$results += "    exit $code ; DATA RACE lines=$races ; log: race-probe-default.log"
$results += ""

# ---------- [4] race online ----------
$out = & go test -tags online -race -p 1 ./core/probe/... -count=1 -v 2>&1
$code = $LASTEXITCODE
Save 'race-probe-online.log' $out
$races = ($out | Where-Object { $_ -match 'DATA RACE' }).Count
$results += "[4] go test -tags online -race -p 1 ./core/probe/... -count=1 -v"
$results += "    exit $code ; DATA RACE lines=$races ; log: race-probe-online.log"
$results += ""

# ---------- [5] targeted repeat race (default) ----------
$runDefault = '^(TestConcurrentStop_RefusesNewRetryAndConverges|TestNewChain_FixedBudgetAndAuthorizationAfterConstruction|TestNewChain_MutablePlanRaceRegression|TestRunStep_ClonesStepInputs|TestPreflight_RejectsVersionedRoles|TestNewChain_RejectsVersionedRoleBeforeAnySend|TestChainRun_VersionedRoleStopReasonIsSanitized|TestW1_RetryReservesAndCommitsEveryAttempt|TestW2_ExhaustedBudgetSendsNothingFurther|TestW3_ExpiredCallerContextStopsBeforeSend|TestW3_DeadlineDuringSendIsNotRetried|TestW3_ConcurrencyLimitAppliesAtProbeEntry|TestW4_UsageSettlement|TestW5_UnexpectedErrorStopsSubsequentSteps|TestRedirect_ProhibitedIsNotRetried|TestStopReasonIsSanitized|TestPreflight_InvalidLaterStepSendsNothing)$'
$out = & go test -race -p 1 ./core/probe/ -run $runDefault -count=5 -v 2>&1
$code = $LASTEXITCODE
Save 'race-targeted-repeat-default.log' $out
$races = ($out | Where-Object { $_ -match 'DATA RACE' }).Count
$results += "[5] go test -race -p 1 ./core/probe/ -count=5 -v -run '<authorization|concurrent stop|preflight|clone|W1-W5|redirect|stop reason>'"
$results += "    exit $code ; DATA RACE lines=$races ; log: race-targeted-repeat-default.log"
$results += ""

# ---------- [6] targeted repeat race (online) ----------
$runOnline = '^(TestOnlineBuild_DirectNewChainRequiresAuthorization|TestOnlineBuild_PreflightRejectsVersionedLaterRoleBeforeAnySend|TestOnlineBuild_CredentialBoundarySharedByReadinessAndExecution|TestOnlineBuild_HasRemoteCapability|TestOnlineBuild_PlaintextRemoteRejectedBeforeAnySend|TestOnlineBuild_UserInfoEndpointRejectedWithoutEcho|TestOnlineBuild_PreflightRejectsLaterInvalidStepBeforeAnySend|TestOnlineBuild_QuerySecretNotEchoedInEvidence|TestOnlineBuild_CredentialValueNeverAppearsInEvidence|TestOnlineBuild_LoopbackFakeStillRunsThroughRunOnline|TestProxyControl_EnvironmentIsHonored|TestConcurrentStop_RefusesNewRetryAndConverges|TestNewChain_MutablePlanRaceRegression)$'
$out = & go test -tags online -race -p 1 ./core/probe/ -run $runOnline -count=5 -v 2>&1
$code = $LASTEXITCODE
Save 'race-targeted-repeat-online.log' $out
$races = ($out | Where-Object { $_ -match 'DATA RACE' }).Count
$results += "[6] go test -tags online -race -p 1 ./core/probe/ -count=5 -v -run '<online auth|preflight roles|credentials|W1-W5|concurrent stop|clone>'"
$results += "    exit $code ; DATA RACE lines=$races ; log: race-targeted-repeat-online.log"
$results += ""

# ---------- [7] full serial suite (default env) ----------
$env:PATH = ($env:PATH -replace '^C:\\msys64\\ucrt64\\bin;', '')
Remove-Item Env:\CC -ErrorAction SilentlyContinue
$out = & go test -p 1 ./... -count=1 2>&1
$code = $LASTEXITCODE
Save 'test-full-serial.log' $out
$results += "[7] go test -p 1 ./... -count=1   (serial, whole module)"
$results += "    exit $code ; log: test-full-serial.log"
$results += ""

# ---------- [8-11] build / vet / online vet / gofmt ----------
$bv = New-Object System.Collections.ArrayList
$out = & go build ./... 2>&1; $c1 = $LASTEXITCODE; $bv += "`$ go build ./..."; $bv += $out; $bv += "exit $c1"; $bv += ""
$out = & go vet ./... 2>&1; $c2 = $LASTEXITCODE; $bv += "`$ go vet ./..."; $bv += $out; $bv += "exit $c2"; $bv += ""
$out = & go vet -tags online ./... 2>&1; $c3 = $LASTEXITCODE; $bv += "`$ go vet -tags online ./..."; $bv += $out; $bv += "exit $c3"; $bv += ""
$out = & gofmt -l core/probe 2>&1; $c4 = $LASTEXITCODE; $bv += "`$ gofmt -l core/probe"; $bv += $out; $bv += "exit $c4"; $bv += ""
Save 'build-vet-online-vet.log' $bv
$results += "[8] go build ./...                 exit $c1 ; log: build-vet-online-vet.log"
$results += "[9] go vet ./...                   exit $c2 ; same log"
$results += "[10] go vet -tags online ./...     exit $c3 ; same log"
$results += "[11] gofmt -l core/probe           exit $c4 (empty output = formatted) ; same log"
$results += ""

Save 'results-commands.txt' $results
Write-Output "DONE"
$results | ForEach-Object { Write-Output $_ }
