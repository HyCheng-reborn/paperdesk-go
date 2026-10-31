# Stage 2D S-1 第四轮修复（针对 1e81aa4 的第三轮独立复核）：常规验证。
#
# 边界：只用本机 fake / httptest / fake 代理 / 外部 overlay；
# 不访问真实模型、ES、Qdrant、MySQL 或互联网；不执行在线探针。

$ErrorActionPreference = 'Continue'
$root = 'C:\Zhy\code\go-rag'
$outDir = Join-Path $root 'verification\stage2d-fix4-1e81aa4-20261028'

$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOFLAGS = ''
$env:CGO_ENABLED = '1'
$env:CC = 'C:/msys64/ucrt64/bin/gcc.exe'
$env:PATH = 'C:\msys64\ucrt64\bin;' + $env:PATH

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

# 环境记录（版本 / 提交 / 工作区状态）。
$envLog = Join-Path $outDir '00-environment.txt'
"DATE: $(Get-Date -Format o)" | Out-File -FilePath $envLog -Encoding utf8
"ROOT: $root" | Out-File -FilePath $envLog -Encoding utf8 -Append
(& go version) | Out-File -FilePath $envLog -Encoding utf8 -Append
(& "$env:CC" --version | Select-Object -First 1) | Out-File -FilePath $envLog -Encoding utf8 -Append
"GOPROXY=$env:GOPROXY GOSUMDB=$env:GOSUMDB CGO_ENABLED=$env:CGO_ENABLED CC=$env:CC" | Out-File -FilePath $envLog -Encoding utf8 -Append
(& git -C $root rev-parse HEAD) | Out-File -FilePath $envLog -Encoding utf8 -Append
(& git -C $root rev-parse HEAD^) | Out-File -FilePath $envLog -Encoding utf8 -Append
(& git -C $root status --porcelain) | Out-File -FilePath $envLog -Encoding utf8 -Append

# 1) 受影响用例（默认构建）：停止/计数/规范化 + 既有回归。
Invoke-Logged '01-default-targeted.txt' @(
    'test', './core/probe', './core/rerank', '-count=1', '-v',
    '-run', 'TestSendGate|TestStepReport_CountsSeparateCommitFromSend|TestPlan_CanonicalIdentityRejectedEverywhere|TestPlan_TargetRulesMatchProduction|TestPreflight_RejectsNilRerankDocumentBeforeAnySend'
)
# 2) probe/rerank 全量（默认构建）。
Invoke-Logged '02-default-full.txt' @('test', './core/probe', './core/rerank', '-count=1')
# 3) 默认构建 race 全量。
Invoke-Logged '03-default-race-full.txt' @('test', '-race', './core/probe', '-count=1')
# 4) 关键停止/计数/规范化场景重复 race。
Invoke-Logged '04-default-race-repeat.txt' @(
    'test', '-race', './core/probe', '-count=5',
    '-run', 'TestSendGate|TestStepReport_CountsSeparateCommitFromSend|TestPlan_CanonicalIdentityRejectedEverywhere'
)
# 5) online 构建全量（对端仍是本机 fake；不执行在线探针）。
Invoke-Logged '05-online-full.txt' @('test', '-tags', 'online', './core/probe', '-count=1')
# 6) online 构建 race 全量。
Invoke-Logged '06-online-race-full.txt' @('test', '-tags', 'online', '-race', './core/probe', '-count=1')
# 7) online 构建关键用例重复 race（目标规则/授权边界）。
Invoke-Logged '07-online-race-repeat.txt' @(
    'test', '-tags', 'online', '-race', './core/probe', '-count=3',
    '-run', 'TestOnlineBuild_TargetRulesReadinessMatchesExecution|TestOnlineBuild_DirectNewChainRequiresAuthorization'
)
# 8) 串行全量。
Invoke-Logged '08-serial-all.txt' @('test', '-p', '1', './...', '-count=1')
# 9-11) 构建与静态检查。
Invoke-Logged '09-build.txt' @('build', './...')
Invoke-Logged '10-vet.txt' @('vet', './...')
Invoke-Logged '11-vet-online.txt' @('vet', '-tags', 'online', './...')
