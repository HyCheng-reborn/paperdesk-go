$ErrorActionPreference = 'Continue'

# Path configuration — override via environment variables; defaults are desensitized placeholders.
$Toolchain = if ($env:RAG_TOOLCHAIN) { $env:RAG_TOOLCHAIN } else { '<TOOLCHAIN>' }

$root = (Get-Location).Path
$r = Join-Path $root 'verification/stage2c3-fix1'
$sha = git rev-parse HEAD
$env:GOFLAGS = ''
# -race requires cgo on windows/amd64. Use the msys64 UCRT64 toolchain and put its
# runtime DLL directory on PATH (otherwise test binaries exit 0xc0000139).
$env:PATH = "$Toolchain\ucrt64\bin;" + $env:PATH
$env:CC = "$Toolchain\ucrt64\bin\gcc.exe"
$env:CXX = "$Toolchain\ucrt64\bin\g++.exe"
$env:CGO_ENABLED = '1'
Set-Location server

@(
  "CODE_SHA: $sha",
  'BASELINE: c4111e031521eb372198d6b428759637cf21fee8',
  'PARENT: 6ab2027ecf8e5cf6664ac751f3a40305772d9ffd',
  'BRANCH: codex/stage2c2-fingerprint',
  ('CWD: ' + (Get-Location).Path),
  'Offline evidence: no real model/ES/Qdrant/MySQL calls; only httptest and local fakes.',
  "CGO toolchain: $Toolchain\ucrt64 (gcc), CGO_ENABLED=1, PATH includes $Toolchain\ucrt64\bin"
) | Set-Content "$r/environment.log"
go version | Add-Content "$r/environment.log"
go env GOOS GOARCH CGO_ENABLED GOFLAGS | Add-Content "$r/environment.log"

$affected = @('./core/config', './core/providers', './core/vector', './core/common', './core', './internal/logic/rag')

$commands = @(
  , (@('focused', 'test') + $affected + @('-run', '^(TestP11_|TestP12_|TestP13_|TestP21_|TestP22_|TestP23_|TestProviderNameNormalization|TestStrictBool_)', '-count=1', '-timeout=300s', '-v'))
  , (@('affected', 'test') + $affected + @('-count=1', '-timeout=300s'))
  , (@('race-affected', 'test', '-race') + $affected + @('-count=1', '-timeout=600s'))
  , @('race-repeat-budget', 'test', '-race', './core/common', '-run', '^(TestP11_|TestP12_|TestP13_|TestP22_|TestProviderNameNormalization)', '-count=3', '-timeout=300s', '-v')
  , @('race-repeat-yaml', 'test', '-race', './internal/logic/rag', '-run', '^(TestP21_|TestP23_|TestP13_FactoryPath)', '-count=3', '-timeout=300s', '-v')
  , @('race-repeat-strictbool', 'test', '-race', './core/config', '-run', '^TestStrictBool_', '-count=5', '-timeout=180s', '-v')
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
  go @a 2>&1 | Out-File $log -Append -Encoding utf8
  $rc = $LASTEXITCODE
  "EXIT: $rc" | Add-Content $log
  Write-Output "$n EXIT $rc"
  if ($rc -ne 0) { exit $rc }
}
