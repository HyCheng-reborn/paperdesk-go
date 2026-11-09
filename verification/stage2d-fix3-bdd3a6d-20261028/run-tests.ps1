# Stage 2D S-1 第三轮修复（复核 bdd3a6d 三项问题）自测脚本。
#
# 全部对端为本机 fake（fake 模型 / fake ES / fake 代理），不访问互联网、
# 不调用真实模型、ES、Qdrant、MySQL，不执行在线探针。
#
# 用法（PowerShell 5.1）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File run-tests.ps1
#
# 每个用例单独写日志，日志尾部记录 EXIT=<真实退出码>。

. "$PSScriptRoot\..\_env-setup.ps1"
$Workspace = Test-RequiredEnv -Name 'RAG_WORKSPACE' -Placeholder '<WORKSPACE>'
$Toolchain = Test-RequiredEnv -Name 'RAG_TOOLCHAIN' -Placeholder '<TOOLCHAIN>'

$ErrorActionPreference = 'Continue'
$root = $Workspace
$outDir = Join-Path $root 'verification\stage2d-fix3-bdd3a6d-20261028'

# 离线：只使用本地 module cache。
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOFLAGS = ''

# UCRT64 gcc 用于 -race（Windows/amd64）。
$env:CGO_ENABLED = '1'
$env:CC = "$Toolchain/ucrt64/bin/gcc.exe"
$env:PATH = "$Toolchain\ucrt64\bin;" + $env:PATH

Set-Location (Join-Path $root 'server')

function Invoke-Logged {
    param(
        [string]$FileName,
        [string[]]$GoArgs
    )
    $log = Join-Path $outDir $FileName
    $cmdLine = 'go ' + ($GoArgs -join ' ')
    "CMD: $cmdLine" | Out-File -FilePath $log -Encoding utf8
    "WORKDIR: $((Get-Location).Path)" | Out-File -FilePath $log -Encoding utf8 -Append
    $out = & go @GoArgs 2>&1
    $code = $LASTEXITCODE
    $out | Out-File -FilePath $log -Encoding utf8 -Append
    "EXIT=$code" | Out-File -FilePath $log -Encoding utf8 -Append
    Write-Host ("[{0}] exit={1}" -f $FileName, $code)
}

$targeted = 'TestSendGate|TestPreflight_RejectsNilRerankDocumentBeforeAnySend|TestPlan_TargetRulesMatchProduction|TestConcurrentStop_RefusesNewRetryAndConverges|TestW[1-5]_|TestProbeChain_GuardRejectsReplacedTarget|TestRedirect_ProhibitedIsNotRetried|TestStopReasonIsSanitized'

Invoke-Logged '01-default-targeted.txt' @('test', '-p', '1', '-count=1', '-v', '-run', $targeted, './core/probe/')
Invoke-Logged '02-default-probe-rerank-full.txt' @('test', '-p', '1', '-count=1', './core/probe/...', './core/rerank/...')
Invoke-Logged '03-default-race-full.txt' @('test', '-race', '-p', '1', '-count=1', './core/probe/...', './core/rerank/...')
Invoke-Logged '04-default-race-repeat.txt' @('test', '-race', '-count=5', '-run', $targeted, './core/probe/')
Invoke-Logged '05-online-full.txt' @('test', '-tags', 'online', '-p', '1', '-count=1', './core/probe/...')
Invoke-Logged '06-online-race-full.txt' @('test', '-tags', 'online', '-race', '-p', '1', '-count=1', './core/probe/...')
Invoke-Logged '07-online-race-repeat.txt' @('test', '-tags', 'online', '-race', '-count=5', '-run', 'TestOnlineBuild|TestSendGate|TestW[1-5]_', './core/probe/')
Invoke-Logged '08-serial-all.txt' @('test', '-p', '1', '-count=1', './...')
Invoke-Logged '09-build.txt' @('build', './...')
Invoke-Logged '10-vet.txt' @('vet', './...')
Invoke-Logged '11-vet-online.txt' @('vet', '-tags', 'online', './core/probe/...')
