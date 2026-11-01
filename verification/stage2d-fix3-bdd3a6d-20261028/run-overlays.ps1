# Stage 2D S-1 第三轮修复：补充复现（overlay）与变异非空转验证。
#
# 复现来源（只读，不修改）：
#   verification/independent-2d-bdd3a6-concurrency/{review_stop_20k_test.go,review_stop_test.go,nil_doc_test.go}
#   verification/independent-2d-bdd3a6-auth/review_test.go
#
# overlay 的 key 指向本仓库包内的虚拟文件名（或既有源文件），value 指向上述
# 未修改的复核源码；因此复跑的是复核方原始反例，而不是重写过的测试。
# 复核方当时在 detached worktree `.review-stage2c1-2dc0b82` 内运行，key 是该
# worktree 的路径；本次在真实工作区复跑，等价地换成真实工作区的包内路径。
#
# 预期：
#   - 停止窗口复现（20-21）：exit 0，命中数恒为 0，最终 SKIP（"timing-dependent
#     window not observed"）；"PASS" 本身不证明窗口已关闭，需看 hits 与 SKIP。
#   - nil 文档 / 非法目标 / dot 相似度（22-25）：反例期望“缺陷存在”，修复后
#     在各自断言处 FAIL，exit 1——这是“缺陷不再复现”的证据。
#   - 变异（30-32）：剥离修复后对应用例必须 FAIL，证明用例不是空转。

$ErrorActionPreference = 'Continue'
$root = 'C:\Zhy\code\go-rag'
$outDir = Join-Path $root 'verification\stage2d-fix3-bdd3a6d-20261028'
$reviewConcurrency = Join-Path $root 'verification\independent-2d-bdd3a6-concurrency'
$reviewAuth = Join-Path $root 'verification\independent-2d-bdd3a6-auth'

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

function Write-Overlay {
    param([string]$Name, [string]$Key, [string]$Value)
    # JSON 字符串里不能直接写 Windows 反斜杠路径；统一转成正斜杠。
    $Key = $Key -replace '\\', '/'
    $Value = $Value -replace '\\', '/'
    $json = '{' + "`n" + '  "Replace": {' + "`n" + '    "' + $Key + '": "' + $Value + '"' + "`n" + '  }' + "`n" + '}'
    # 注意：overlay JSON 不能带 UTF-8 BOM（go 会报 invalid character '\ufeff'）。
    [System.IO.File]::WriteAllText((Join-Path $outDir $Name), $json, (New-Object System.Text.UTF8Encoding($false)))
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

# ---- 补充复现（复核方原始源码，未修改）----
$ovStop20k = Join-Path $outDir 'overlay-repro-stop20k.json'
Write-Overlay 'overlay-repro-stop20k.json' 'C:/Zhy/code/go-rag/server/core/probe/independent_review_stop_test.go' (Join-Path $reviewConcurrency 'review_stop_20k_test.go')
$ovStop100k = Join-Path $outDir 'overlay-repro-stop100k.json'
Write-Overlay 'overlay-repro-stop100k.json' 'C:/Zhy/code/go-rag/server/core/probe/independent_review_stop_test.go' (Join-Path $reviewConcurrency 'review_stop_test.go')
$ovNilDoc = Join-Path $outDir 'overlay-repro-nildoc.json'
Write-Overlay 'overlay-repro-nildoc.json' 'C:/Zhy/code/go-rag/server/core/probe/independent_nil_doc_test.go' (Join-Path $reviewConcurrency 'nil_doc_test.go')
$ovAuth = Join-Path $outDir 'overlay-repro-auth.json'
Write-Overlay 'overlay-repro-auth.json' 'C:/Zhy/code/go-rag/server/core/probe/independent_auth_bdd_test.go' (Join-Path $reviewAuth 'review_test.go')

Invoke-Logged '20-repro-stop-window-20k.txt' @('test', '-race', '-overlay', $ovStop20k, '-p', '1', './core/probe', '-run', '^TestReviewStopMarkedBeforeCancellationAllowsSend$', '-count=5', '-v')
Invoke-Logged '21-repro-stop-window-100k.txt' @('test', '-race', '-overlay', $ovStop100k, '-p', '1', './core/probe', '-run', '^TestReviewStopMarkedBeforeCancellationAllowsSend$', '-count=1', '-v')
Invoke-Logged '22-repro-nil-rerank-doc.txt' @('test', '-overlay', $ovNilDoc, '-p', '1', './core/probe', '-run', '^TestReviewNilRerankDocumentPassesPreflight$', '-count=1', '-v')
Invoke-Logged '23-repro-auth-nil-doc.txt' @('test', '-tags', 'online', '-race', '-overlay', $ovAuth, '-p', '1', './core/probe', '-run', '^TestReviewNilRerankDocumentPassesPreflight$', '-count=1', '-v')
Invoke-Logged '24-repro-auth-invalid-target.txt' @('test', '-tags', 'online', '-race', '-overlay', $ovAuth, '-p', '1', './core/probe', '-run', '^TestReviewInvalidPhysicalTargetReportedReady$', '-count=1', '-v')
Invoke-Logged '25-repro-auth-mismatched-similarity.txt' @('test', '-tags', 'online', '-race', '-overlay', $ovAuth, '-p', '1', './core/probe', '-run', '^TestReviewMismatchedSimilarityReportedReady$', '-count=1', '-v')

# ---- 变异：剥离修复后，对应用例必须失败（非空转证据）----
$ovMutSend = Join-Path $outDir 'overlay-mutation-sendgate.json'
Write-Overlay 'overlay-mutation-sendgate.json' 'C:/Zhy/code/go-rag/server/core/probe/sendgate.go' (Join-Path $outDir 'mutation-sendgate-no-commit.go')
$ovMutPre = Join-Path $outDir 'overlay-mutation-preflight.json'
Write-Overlay 'overlay-mutation-preflight.json' 'C:/Zhy/code/go-rag/server/core/probe/preflight.go' (Join-Path $outDir 'mutation-preflight-no-rerank-validation.go')
$ovMutProbe = Join-Path $outDir 'overlay-mutation-probe.json'
Write-Overlay 'overlay-mutation-probe.json' 'C:/Zhy/code/go-rag/server/core/probe/probe.go' (Join-Path $outDir 'mutation-probe-no-target-identity.go')

Invoke-Logged '30-mutation-sendgate-boundary.txt' @('test', '-overlay', $ovMutSend, '-p', '1', './core/probe', '-run', '^TestSendGate_TransportBoundaryRefusesAfterStopMarker$', '-count=1', '-v')
# 诚实记录：端到端固定窗口用例在剥离闸门后仍可能因“ctx 已被 stop 取消”而未发送，
# 因此它不能单独区分“有闸门”与“只靠 ctx 取消”；非空转证明依赖上面的传输边界用例。
Invoke-Logged '30b-mutation-sendgate-admitted-unsent.txt' @('test', '-overlay', $ovMutSend, '-p', '1', './core/probe', '-run', '^TestSendGate_StopMarkerRefusesAdmittedButUnsentAttempt$', '-count=1', '-v')
# 原复核形状（20k 人工句柄）：剥离闸门后，若取消尚未轮到该步骤，fake 模型会收到请求。
Invoke-Logged '30c-mutation-sendgate-review-shape.txt' @('test', '-overlay', $ovMutSend, '-p', '1', './core/probe', '-run', '^TestSendGate_OriginalReviewStopWindowShapeHasNoSend$', '-count=3', '-v')
Invoke-Logged '31-mutation-preflight.txt' @('test', '-overlay', $ovMutPre, '-p', '1', './core/probe', '-run', '^TestPreflight_RejectsNilRerankDocumentBeforeAnySend$', '-count=1', '-v')
Invoke-Logged '32-mutation-target.txt' @('test', '-overlay', $ovMutProbe, '-p', '1', './core/probe', '-run', '^TestPlan_TargetRulesMatchProduction$', '-count=1', '-v')
