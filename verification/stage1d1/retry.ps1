Set-Location "c:\Zhy\code\go-rag\server"
$logDir = "c:\Zhy\code\go-rag\verification\stage1d1"

# Retry race-core
Write-Host ">>> Retrying: go test -race ./core -count=1"
$output = & cmd /c "go test -race ./core -count=1 2>&1"
$exitCode = $LASTEXITCODE
$meta = "# CMD: go test -race ./core -count=1`n# GOFLAGS: $env:GOFLAGS`n# EXIT: $exitCode`n# DATE: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')`n`n"
$meta + ($output -join "`n") | Out-File -FilePath "$logDir\race-core.log" -Encoding utf8
Write-Host "    race-core EXIT: $exitCode"

# Retry race-knowledge-stress
Write-Host ">>> Retrying: go test -race ./internal/logic/knowledge/... -count=10 -v -run TestSaveChunks_"
$output = & cmd /c "go test -race ./internal/logic/knowledge/... -count=10 -v -run TestSaveChunks_ 2>&1"
$exitCode = $LASTEXITCODE
$meta = "# CMD: go test -race ./internal/logic/knowledge/... -count=10 -v -run TestSaveChunks_`n# GOFLAGS: $env:GOFLAGS`n# EXIT: $exitCode`n# DATE: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')`n`n"
$meta + ($output -join "`n") | Out-File -FilePath "$logDir\race-knowledge-stress.log" -Encoding utf8
Write-Host "    race-knowledge-stress EXIT: $exitCode"
