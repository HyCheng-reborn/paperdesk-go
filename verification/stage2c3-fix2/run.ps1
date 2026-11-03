$ErrorActionPreference = 'Continue'
$root = (Get-Location).Path
$r = Join-Path $root 'verification/stage2c3-fix2'
$sha = git rev-parse HEAD
$env:GOFLAGS = ''
# -race requires cgo on windows/amd64. Use the msys64 UCRT64 toolchain and put its
# runtime DLL directory on PATH (otherwise test binaries exit 0xc0000139).
$env:PATH = '<TOOLCHAIN>\ucrt64\bin;' + $env:PATH
$env:CC = '<TOOLCHAIN>\ucrt64\bin\gcc.exe'
$env:CXX = '<TOOLCHAIN>\ucrt64\bin\g++.exe'
$env:CGO_ENABLED = '1'
# Windows 下 %TEMP% 中的测试二进制会被外部进程（杀毒/索引器）短时占用，
# 导致 `go` 清理阶段报 `unlinkat ... Access is denied`。改用仓库外的独立临时目录。
$env:GOTMPDIR = '<TEMP>'
New-Item -ItemType Directory -Force -Path $env:GOTMPDIR | Out-Null
Set-Location server

@(
  "CODE_SHA: $sha",
  'BASELINE: 2c952ce5e4afe3096945596c9b03be1b5cf1458b',
  'PARENT: c4111e031521eb372198d6b428759637cf21fee8',
  'BRANCH: codex/stage2c2-fingerprint',
  ('CWD: ' + (Get-Location).Path),
  'Offline evidence: no real model/ES/Qdrant/MySQL calls; only in-memory transport, httptest and local fakes.',
  'CGO toolchain: <TOOLCHAIN>\ucrt64 (gcc), CGO_ENABLED=1, PATH includes <TOOLCHAIN>\ucrt64\bin'
  'GOTMPDIR: <TEMP> (temp exe cleanup failures in %TEMP% are an environment limit)'
) | Set-Content "$r/environment.log"
go version | Add-Content "$r/environment.log"
go env GOOS GOARCH CGO_ENABLED GOFLAGS | Add-Content "$r/environment.log"

$affected = @('./core/providers', './core/providers/bailian', './core/vector', './core/common', './core', './internal/logic/rag')
$focusedRun = '^(TestRequestModelIDRejectsVersionedIdentity|TestP1_|TestP2_|TestP12_|TestR2_|TestNewProviderEmbedding_|TestComputeFingerprintMeta)'

$commands = @(
  , (@('focused', 'test', '-p', '1') + $affected + @('-run', $focusedRun, '-count=1', '-timeout=300s', '-v'))
  , (@('affected', 'test', '-p', '1') + $affected + @('-count=1', '-timeout=900s'))
  , (@('race-affected', 'test', '-p', '1', '-race') + $affected + @('-count=1', '-timeout=1200s'))
  , @('race-repeat-identity', 'test', '-race', './core/providers', './core/providers/bailian', './core/common', './core', '-run', $focusedRun, '-count=3', '-timeout=600s', '-v')
  , @('race-repeat-factory', 'test', '-race', './internal/logic/rag', '-run', '^(TestP1_|TestP2_|TestP13_)', '-count=3', '-timeout=600s', '-v')
  , @('full', 'test', '-p', '1', './...', '-count=1', '-timeout=600s')
  , @('vet', 'vet', './...')
  , @('build', 'build', './...')
)
foreach ($c in $commands) {
  $n = $c[0]
  $a = $c[1..($c.Length - 1)]
  $log = "$r/$n.log"
  @(
    "CODE_SHA: $sha",
    ('CWD: ' + (Get-Location).Path),
    ('CMD: go ' + ($a -join ' ')),
    ('DATE: ' + (Get-Date -Format o))
  ) | Set-Content $log
  $rc = 1
  # 本机（Windows + 外部扫描进程）偶发在 fork/exec 或 unlinkat 测试二进制时报
  # `Access is denied`。这不是测试失败：全部用例尚未执行或已 PASS。仅对这种
  # 环境级文件锁做有限重试，并逐次记录 ATTEMPT/EXIT；真实失败不重试。
  for ($attempt = 1; $attempt -le 3; $attempt++) {
    "ATTEMPT: $attempt" | Add-Content $log
    go @a 2>&1 | Out-File $log -Append -Encoding utf8
    $rc = $LASTEXITCODE
    "ATTEMPT ${attempt} EXIT: $rc" | Add-Content $log
    if ($rc -eq 0) { break }
    # 控制台宽度可能把错误文本拆行，因此不直接匹配整句；改为“没有任何真实
    # 测试失败标记”才视为环境级问题。
    $text = Get-Content $log -Raw
    if ($text -match '(?m)^(--- FAIL|FAIL\s+github|panic:)') { break }
    Start-Sleep -Seconds 3
  }
  "EXIT: $rc" | Add-Content $log
  Write-Output "$n EXIT $rc"
  if ($rc -ne 0) { exit $rc }
}
