$ErrorActionPreference = 'Continue'
Set-Location "c:\Zhy\code\go-rag\server"
$logDir = "c:\Zhy\code\go-rag\verification\stage1d1"

function Run-Cmd {
    param([string]$cmd, [string]$logFile)
    Write-Host ">>> $cmd" -ForegroundColor Cyan
    $output = & cmd /c "$cmd 2>&1"
    $exitCode = $LASTEXITCODE
    $meta = "# CMD: $cmd`n# GOFLAGS: $env:GOFLAGS`n# EXIT: $exitCode`n# DATE: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')`n`n"
    $content = $meta + ($output -join "`n")
    $content | Out-File -FilePath (Join-Path $logDir $logFile) -Encoding utf8
    Write-Host "    EXIT: $exitCode" -ForegroundColor $(if ($exitCode -eq 0) { 'Green' } else { 'Red' })
    return $exitCode
}

$results = @()

# 1. gofmt
$e = Run-Cmd "gofmt -l internal/logic/knowledge/chunks.go internal/logic/knowledge/chunks_test.go core/rag.go core/indexer.go core/indexer_test.go" "gofmt.log"
$results += [PSCustomObject]@{Step="gofmt"; Exit=$e}

# 2. go build
$e = Run-Cmd "go build ./..." "build.log"
$results += [PSCustomObject]@{Step="go build"; Exit=$e}

# 3. go vet
$e = Run-Cmd "go vet ./..." "vet.log"
$results += [PSCustomObject]@{Step="go vet"; Exit=$e}

# 4. knowledge package tests
$e = Run-Cmd "go test ./internal/logic/knowledge/... -count=1 -v" "test-knowledge.log"
$results += [PSCustomObject]@{Step="test-knowledge"; Exit=$e}

# 5. core package tests (full)
$e = Run-Cmd "go test ./core -count=1 -v" "test-core.log"
$results += [PSCustomObject]@{Step="test-core"; Exit=$e}

# 6. core race detection
$e = Run-Cmd "go test -race ./core -count=1" "race-core.log"
$results += [PSCustomObject]@{Step="race-core"; Exit=$e}

# 7. knowledge race detection
$e = Run-Cmd "go test -race ./internal/logic/knowledge/... -count=1" "race-knowledge.log"
$results += [PSCustomObject]@{Step="race-knowledge"; Exit=$e}

# 8a. knowledge stress test
$e = Run-Cmd "go test -race ./internal/logic/knowledge/... -count=10 -v -run TestSaveChunks_" "race-knowledge-stress.log"
$results += [PSCustomObject]@{Step="race-knowledge-stress"; Exit=$e}

# 8b. core orchestration stress test
$e = Run-Cmd "go test -race ./core -count=10 -v -run TestIndexAsyncByDocsID_" "race-core-orchestration-stress.log"
$results += [PSCustomObject]@{Step="race-core-orch-stress"; Exit=$e}

# 9. full project tests
$e = Run-Cmd "go test -p 1 ./... -count=1" "test-full.log"
$results += [PSCustomObject]@{Step="test-full"; Exit=$e}

Write-Host "`n========== SUMMARY ==========" -ForegroundColor Yellow
$results | Format-Table -AutoSize
Write-Host "Done."
