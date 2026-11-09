# Stage 2D S-1 第五批：复用第四轮独立复核的补充复现与变异验证。
#
# 复现来源（只读，不修改）：
#   verification/independent-2d-91c872-counts/{overlay.json,overlay-openai.json,
#       review_counts_test.go,review_openai_test.go}
#   verification/independent-2d-91c872-stopreview/{overlay.json,budget_after_stop_test.go}
# 基线 worktree（只读，detached HEAD = 固定起点 91c872f）：
#   .review-stage2c1-2dc0b82/server
#
# 预期：
#   - 20（基线=固定起点，复核原始 bailian 计数用例）：exit 1——
#     `*bailian.validatedEmbedRole does not expose verifiable provider identity`。
#   - 21（基线 openai 对照）：exit 0，1/1/1。证明基线的失败只来自百炼身份接口，
#     共享路径本身正常。
#   - 22（同一复核源码 overlay 到本批修复后的工作区）：exit 0，1/1/1 ×3。
#     这是"复核方原始反例在修复后转为通过"的证据，不是实现者自测。
#   - 23（修复后 openai 对照）：exit 0，1/1/1 ×3。
#   - 24（复核 beforeCancel 预算复现）：exit 0，观测 before=0/after=1、modelHits=0。
#     该日志只记录"标记后仍可能新增预算承诺"的事实，不声称概率。
#   - 30/31（变异）：exit 1——把 Profile() 变成零身份 / 浅拷贝后，
#     22 与防御性复制用例必须失败，证明这两个用例不是空转。

. "$PSScriptRoot\..\_env-setup.ps1"
$Workspace = Test-RequiredEnv -Name 'RAG_WORKSPACE' -Placeholder '<WORKSPACE>'
$Toolchain = Test-RequiredEnv -Name 'RAG_TOOLCHAIN' -Placeholder '<TOOLCHAIN>'

$ErrorActionPreference = 'Continue'
$root = $Workspace
$outDir = Join-Path $root 'verification\stage2d-fix5-91c872-20261029'
$reviewCounts = Join-Path $root 'verification\independent-2d-91c872-counts'
$reviewStop = Join-Path $root 'verification\independent-2d-91c872-stopreview'
$baselineServer = Join-Path $root '.review-stage2c1-2dc0b82\server'

$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOFLAGS = ''
$env:CGO_ENABLED = '1'
$env:CC = "$Toolchain/ucrt64/bin/gcc.exe"
$env:PATH = "$Toolchain\ucrt64\bin;" + $env:PATH

function Invoke-Logged {
    param(
        [string]$FileName,
        [string]$WorkDir,
        [string[]]$GoArgs
    )
    $log = Join-Path $outDir $FileName
    $cmdLine = 'go ' + ($GoArgs -join ' ')
    "CMD: $cmdLine" | Out-File -FilePath $log -Encoding utf8
    "WORKDIR: $WorkDir" | Out-File -FilePath $log -Encoding utf8 -Append
    Push-Location $WorkDir
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
    Pop-Location
    $out | Out-File -FilePath $log -Encoding utf8 -Append
    "EXIT=$code" | Out-File -FilePath $log -Encoding utf8 -Append
    Write-Host ("[{0}] exit={1}" -f $FileName, $code)
}

function Write-Overlay {
    param([string]$Name, [string]$Key, [string]$Value)
    Write-OverlayMulti $Name @($Key) @($Value)
}

function Write-OverlayMulti {
    param([string]$Name, [string[]]$Keys, [string[]]$Values)
    if ($Keys.Count -ne $Values.Count) { throw "overlay mapping arity mismatch: $Name" }
    # JSON 字符串里不能直接写 Windows 反斜杠路径；统一转成正斜杠。
    $entries = @()
    for ($i = 0; $i -lt $Keys.Count; $i++) {
        $k = $Keys[$i] -replace '\\', '/'
        $v = $Values[$i] -replace '\\', '/'
        $entries += ('    "' + $k + '": "' + $v + '"')
    }
    $json = '{' + "`n" + '  "Replace": {' + "`n" + ($entries -join ("," + "`n")) + "`n" + '  }' + "`n" + '}'
    # 注意：overlay JSON 不能带 UTF-8 BOM（go 会报 invalid character '\ufeff'）。
    [System.IO.File]::WriteAllText((Join-Path $outDir $Name), $json, (New-Object System.Text.UTF8Encoding($false)))
}

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

# ---- 复核原始源码的 overlay 映射（指向本工作区，key 为包内虚拟文件名）----
Write-Overlay 'overlay-review-bailian-counts.json' `
    "$Workspace/server/core/probe/independent_counts_test.go" `
    (Join-Path $reviewCounts 'review_counts_test.go')
Write-Overlay 'overlay-review-openai-counts.json' `
    "$Workspace/server/core/probe/independent_counts_test.go" `
    (Join-Path $reviewCounts 'review_openai_test.go')
Write-Overlay 'overlay-review-budget-stop.json' `
    "$Workspace/server/core/probe/independent_budget_stop_test.go" `
    (Join-Path $reviewStop 'budget_after_stop_test.go')

$ovBailian = Join-Path $outDir 'overlay-review-bailian-counts.json'
$ovOpenAI = Join-Path $outDir 'overlay-review-openai-counts.json'
$ovBudgetStop = Join-Path $outDir 'overlay-review-budget-stop.json'
# 基线复用复核方自己的 overlay（key 指向 .review-stage2c1-2dc0b82 worktree）。
$ovBaseBailian = Join-Path $reviewCounts 'overlay.json'
$ovBaseOpenAI = Join-Path $reviewCounts 'overlay-openai.json'

# ---- 基线（固定起点 91c872f，只读 worktree）：失败必须可复现 ----
Invoke-Logged '20-baseline-bailian-counts.txt' $baselineServer @(
    'test', '-overlay', $ovBaseBailian, '-p', '1', './core/probe',
    '-run', '^TestReviewBailianTraceCountsRealAdapters$', '-count=3', '-v'
)
# ---- 基线 openai 对照：必须通过，证明失败只来自百炼身份 ----
Invoke-Logged '21-baseline-openai-counts.txt' $baselineServer @(
    'test', '-overlay', $ovBaseOpenAI, '-p', '1', './core/probe',
    '-run', '^TestReviewOpenAITraceCountsRealAdapters$', '-count=3', '-v'
)
# ---- 修复后：同一复核源码在本工作区 ----
Invoke-Logged '22-fixed-bailian-counts.txt' (Join-Path $root 'server') @(
    'test', '-overlay', $ovBailian, '-p', '1', './core/probe',
    '-run', '^TestReviewBailianTraceCountsRealAdapters$', '-count=3', '-v'
)
Invoke-Logged '23-fixed-openai-counts.txt' (Join-Path $root 'server') @(
    'test', '-overlay', $ovOpenAI, '-p', '1', './core/probe',
    '-run', '^TestReviewOpenAITraceCountsRealAdapters$', '-count=3', '-v'
)
# ---- 复核 beforeCancel 预算复现（记录事实，不声称概率）----
Invoke-Logged '24-review-budget-after-stop.txt' (Join-Path $root 'server') @(
    'test', '-overlay', $ovBudgetStop, '-p', '1', './core/probe',
    '-run', '^TestReviewBudgetCommitAfterStop$', '-count=3', '-v'
)

# ---- 变异：Profile() 变为零身份 / 浅拷贝后，用例必须失败 ----
$embeddingPath = Join-Path $root 'server\core\providers\bailian\embedding.go'
$needle = 'return *v.profile.Clone()'
$source = [System.IO.File]::ReadAllText($embeddingPath)
if (-not $source.Contains($needle)) {
    throw "mutation anchor not found in $embeddingPath"
}
$zeroProfile = $source.Replace($needle, 'return providers.ProviderProfile{}')
$shallowProfile = $source.Replace($needle, 'return *v.profile')
[System.IO.File]::WriteAllText((Join-Path $outDir 'mutation-bailian-zero-profile.go'), $zeroProfile, $utf8NoBom)
[System.IO.File]::WriteAllText((Join-Path $outDir 'mutation-bailian-shallow-profile.go'), $shallowProfile, $utf8NoBom)

# 注意：go 只接受一个 -overlay 参数（后一个覆盖前一个），因此变异与复核用例
# 必须合并进同一张映射表，否则变异不生效（本目录首轮即因写成两个 -overlay 而空转）。
Write-OverlayMulti 'overlay-mutation-zero-profile-with-review.json' @(
    "$Workspace/server/core/providers/bailian/embedding.go",
    "$Workspace/server/core/probe/independent_counts_test.go"
) @(
    (Join-Path $outDir 'mutation-bailian-zero-profile.go'),
    (Join-Path $reviewCounts 'review_counts_test.go')
)
Write-Overlay 'overlay-mutation-bailian-shallow-profile.json' `
    "$Workspace/server/core/providers/bailian/embedding.go" `
    (Join-Path $outDir 'mutation-bailian-shallow-profile.go')

$ovMutZero = Join-Path $outDir 'overlay-mutation-zero-profile-with-review.json'
$ovMutShallow = Join-Path $outDir 'overlay-mutation-bailian-shallow-profile.json'

# 30：零身份 → 复核原始用例必须重新失败（身份门禁未被放宽）。
Invoke-Logged '30-mutation-bailian-zero-profile.txt' (Join-Path $root 'server') @(
    'test', '-overlay', $ovMutZero, '-p', '1', './core/probe',
    '-run', '^TestReviewBailianTraceCountsRealAdapters$', '-count=1', '-v'
)
# 31：浅拷贝 → 防御性复制用例必须失败（副本不是空转断言）。
Invoke-Logged '31-mutation-bailian-shallow-profile.txt' (Join-Path $root 'server') @(
    'test', '-overlay', $ovMutShallow, '-p', '1', './core/probe',
    '-run', '^TestBailianEmbeddingProfileIsDefensiveCopy$', '-count=1', '-v'
)
