# Stage 2D S-1 第五批（针对 91c872f 的第四轮独立复核）：常规验证。
#
# 边界：只用本机 fake / httptest / 外部 overlay；
# 不访问真实模型、ES、Qdrant、MySQL 或互联网；不执行在线探针。
#
# 受影响范围：百炼 embedding 身份接口（core/providers/bailian）、
# 共享构造门禁（core/common）、探针链（core/probe）。

$ErrorActionPreference = 'Continue'
$root = 'C:\Zhy\code\go-rag'
$outDir = Join-Path $root 'verification\stage2d-fix5-91c872-20261029'

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
    # Windows 上偶发"测试二进制被其它进程占用"（杀毒/索引），与被测断言无关；
    # 只对这种可识别的瞬时错误重试，并在日志里逐次记录，不掩盖真实失败。
    $code = 1
    for ($attempt = 1; $attempt -le 12; $attempt++) {
        $out = & go @GoArgs 2>&1
        $code = $LASTEXITCODE
        if ($code -eq 0) { break }
        $text = ($out | Out-String)
        if ($text -notmatch 'being used by another process') { break }
        "ATTEMPT ${attempt}: transient windows binary lock, retrying" | Out-File -FilePath $log -Encoding utf8 -Append
        Start-Sleep -Seconds 3
    }
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
"HEAD=$(& git -C $root rev-parse HEAD)" | Out-File -FilePath $envLog -Encoding utf8 -Append
"HEAD^=$(& git -C $root rev-parse HEAD^)" | Out-File -FilePath $envLog -Encoding utf8 -Append
"BRANCH=$(& git -C $root rev-parse --abbrev-ref HEAD)" | Out-File -FilePath $envLog -Encoding utf8 -Append
(& git -C $root status --porcelain) | Out-File -FilePath $envLog -Encoding utf8 -Append

# 1) 受影响用例（默认构建）：百炼身份 + 预算承诺 + 既有停止/计数回归。
Invoke-Logged '01-default-targeted.txt' @(
    'test', './core/probe', '-count=1', '-v',
    '-run', 'TestProviderRegistrationBuildsProductionChain|TestBailianEmbeddingProfileIsDefensiveCopy|TestBailianEmbeddingIdentityMismatchFailsClosed|TestBailianEmbeddingCancelAndConcurrentBudget|TestStopMarkerDoesNotRetractBudgetCommit|TestSendGate|TestStepReport_CountsSeparateCommitFromSend'
)
# 2) 受影响包全量（默认构建）：probe + providers + common。
Invoke-Logged '02-default-full.txt' @('test', './core/probe', './core/providers/...', './core/common', '-count=1')
# 3) 默认构建 race 全量（受影响包）。
Invoke-Logged '03-default-race-full.txt' @('test', '-race', './core/probe', './core/providers/...', './core/common', '-count=1')
# 4) 关键身份/预算/取消/停止场景重复 race。
Invoke-Logged '04-default-race-repeat.txt' @(
    'test', '-race', './core/probe', '-count=5', '-v',
    '-run', 'TestProviderRegistrationBuildsProductionChain|TestBailianEmbeddingProfileIsDefensiveCopy|TestBailianEmbeddingIdentityMismatchFailsClosed|TestBailianEmbeddingCancelAndConcurrentBudget|TestStopMarkerDoesNotRetractBudgetCommit'
)
# 5) online 构建全量（对端仍是本机 fake；不执行在线探针）。
Invoke-Logged '05-online-full.txt' @('test', '-tags', 'online', './core/probe', '-count=1')
# 6) online 构建 race 全量。
Invoke-Logged '06-online-race-full.txt' @('test', '-tags', 'online', '-race', './core/probe', '-count=1')
# 7) online 构建关键用例重复 race（身份构造与停止时序）。
Invoke-Logged '07-online-race-repeat.txt' @(
    'test', '-tags', 'online', '-race', './core/probe', '-count=3', '-v',
    '-run', 'TestProviderRegistrationBuildsProductionChain|TestStopMarkerDoesNotRetractBudgetCommit|TestOnlineBuild_DirectNewChainRequiresAuthorization'
)
# 8) 串行全量。
Invoke-Logged '08-serial-all.txt' @('test', '-p', '1', './...', '-count=1')
# 9-11) 构建与静态检查。
Invoke-Logged '09-build.txt' @('build', './...')
Invoke-Logged '10-vet.txt' @('vet', './...')
Invoke-Logged '11-vet-online.txt' @('vet', '-tags', 'online', './...')
