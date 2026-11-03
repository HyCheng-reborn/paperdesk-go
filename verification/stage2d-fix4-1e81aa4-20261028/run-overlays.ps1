# Stage 2D S-1 第四轮修复：复用第三轮独立复核的补充复现与变异验证。
#
# 复现来源（只读，不修改）：
#   verification/independent-2d-1e81aa-gate/{review_gate_test.go,review_gate_ctx_test.go,review_attempt_test.go}
#   verification/independent-2d-1e81aa-validation/{space_model_test.go}
#
# overlay 的 key 指向本仓库包内的虚拟文件名，value 指向上述未修改的复核源码；
# 因此复跑的是复核方原始反例，而不是重写过的测试。
#
# 预期：
#   - 20/21（停止后的 handoff 窗口）：exit 0；每轮记录 hits at stop / hits after release。
#     若 hits after release > 0，即复核窗口仍可观测（本批**未闭合**）；若未观察到则 SKIP。
#   - 22（计数）：exit 0——复核断言 Attempts=1/committed=1/对端 0 依然成立；
#     新增的 GateAdmissions/TransportCalls 由本批自有测试覆盖（见 01/04 日志）。
#   - 23/24（规范化）：exit 1——复核用例的前提“Ready()==true”在修复后为 false，
#     因此在它自己的第一处断言失败；这是“非规范输入已在就绪阶段拒绝”的证据。
#   - 30/31/32（变异）：exit 1——剥离 commitSend 或规范形式校验后，对应用例必须失败，
#     证明这些用例不是空转。

$ErrorActionPreference = 'Continue'
$root = '<WORKSPACE>'
$outDir = Join-Path $root 'verification\stage2d-fix4-1e81aa4-20261028'
$reviewGate = Join-Path $root 'verification\independent-2d-1e81aa-gate'
$reviewValidation = Join-Path $root 'verification\independent-2d-1e81aa-validation'

$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOFLAGS = ''
$env:CGO_ENABLED = '1'
$env:CC = '<TOOLCHAIN>/ucrt64/bin/gcc.exe'
$env:PATH = '<TOOLCHAIN>\ucrt64\bin;' + $env:PATH

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

# ---- 复核原始源码的 overlay 映射 ----
Write-Overlay 'overlay-review-gate.json' '<WORKSPACE>/server/core/probe/independent_review_gate_test.go' (Join-Path $reviewGate 'review_gate_test.go')
Write-Overlay 'overlay-review-gate-ctx.json' '<WORKSPACE>/server/core/probe/independent_review_gate_ctx_test.go' (Join-Path $reviewGate 'review_gate_ctx_test.go')
Write-Overlay 'overlay-review-attempt.json' '<WORKSPACE>/server/core/probe/independent_review_attempt_test.go' (Join-Path $reviewGate 'review_attempt_test.go')
Write-Overlay 'overlay-review-space-model.json' '<WORKSPACE>/server/core/probe/independent_review_space_model_test.go' (Join-Path $reviewValidation 'space_model_test.go')

$ovGate = Join-Path $outDir 'overlay-review-gate.json'
$ovGateCtx = Join-Path $outDir 'overlay-review-gate-ctx.json'
$ovAttempt = Join-Path $outDir 'overlay-review-attempt.json'
$ovSpace = Join-Path $outDir 'overlay-review-space-model.json'

Invoke-Logged '20-review-gate-admission.txt' @('test', '-race', '-overlay', $ovGate, '-p', '1', './core/probe', '-run', '^TestReviewGateAdmissionIsNotPhysicalSend$', '-count=5', '-v')
Invoke-Logged '21-review-gate-ctx.txt' @('test', '-race', '-overlay', $ovGateCtx, '-p', '1', './core/probe', '-run', '^TestReviewGateContextLiveAfterStop$', '-count=5', '-v')
Invoke-Logged '22-review-attempt-count.txt' @('test', '-overlay', $ovAttempt, '-p', '1', './core/probe', '-run', '^TestReviewAttemptEvidenceCountsGateBlockedCall$', '-count=1', '-v')
Invoke-Logged '23-review-space-model.txt' @('test', '-tags', 'online', '-overlay', $ovSpace, '-p', '1', './core/probe', '-run', '^TestReviewSpaceEmbeddingReadyButFailsBeforeModel$', '-count=1', '-v')
Invoke-Logged '24-review-normalized-similarity.txt' @('test', '-tags', 'online', '-overlay', $ovSpace, '-p', '1', './core/probe', '-run', '^TestReviewNormalizedSimilarityReadyButFakeSchemaFails$', '-count=1', '-v')

# ---- 变异：剥离修复后，对应用例必须失败（非空转证据）----
Write-Overlay 'overlay-mutation-sendgate.json' '<WORKSPACE>/server/core/probe/sendgate.go' (Join-Path $outDir 'mutation-sendgate-no-commit.go')
Write-Overlay 'overlay-mutation-canonical.json' '<WORKSPACE>/server/core/probe/probe.go' (Join-Path $outDir 'mutation-probe-no-canonical.go')

$ovMutSend = Join-Path $outDir 'overlay-mutation-sendgate.json'
$ovMutCanon = Join-Path $outDir 'overlay-mutation-canonical.json'

Invoke-Logged '30-mutation-sendgate-boundary.txt' @('test', '-overlay', $ovMutSend, '-p', '1', './core/probe', '-run', '^TestSendGate_TransportBoundaryRefusesAfterStopMarker$', '-count=1', '-v')
Invoke-Logged '31-mutation-sendgate-counts.txt' @('test', '-overlay', $ovMutSend, '-p', '1', './core/probe', '-run', '^TestStepReport_CountsSeparateCommitFromSend$', '-count=1', '-v')
Invoke-Logged '32-mutation-canonical.txt' @('test', '-overlay', $ovMutCanon, '-p', '1', './core/probe', '-run', '^TestPlan_CanonicalIdentityRejectedEverywhere$', '-count=1', '-v')
