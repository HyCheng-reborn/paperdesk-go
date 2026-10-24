$ErrorActionPreference='Continue'
Set-Location C:/Zhy/code/go-rag-review-1d3/server
$dest='C:/Zhy/code/go-rag/verification/independent-1d3-9e59029'
$env:GOPROXY='off'
$env:GOSUMDB='off'
$commands=@(
'build ./...',
'vet ./...',
'test ./core ./internal/logic/rag/... ./internal/cmd/... -count=1 -timeout=120s',
'test -race ./core ./internal/logic/rag/... ./internal/cmd/... -count=1 -timeout=120s',
'test -p 1 ./... -count=1 -timeout=120s',
'test -race ./core -count=10 -v -timeout=180s -run ^(TestIndex_(AdmissionClosed_Rejects|AdmissionFull_Rejects|SyncFail_ReleasesOnce|SyncSuccess_TransfersOwnershipOnce|Shutdown_WaitsAdmittedSyncAndBG|Shutdown_Timeout_KeepsWaiting|Shutdown_Idempotent|SyncPanic_ReleasesCapacityAndRegistration|BackgroundFailure_MarksFailed|BackgroundPanic_MarksFailed|BackgroundTimeout_MarksFailed|BackgroundSuccess_NoMarkFailed|CleanupCtxIndependent)|TestIndexAsyncByDocsID_RetryRespectsContext)$',
'test ./core ./internal/logic/rag/... ./internal/cmd/... -list .'
)
$i=0
foreach($command in $commands){
$i++
$log=Join-Path $dest ('clean-'+$i+'.log')
@('SHA: 9e59029133cfdb6b42a449f8dcb625c3708aeaa1','CWD: '+(Get-Location),'CMD: go '+$command,'GOPROXY=off GOSUMDB=off',('START: '+(Get-Date -Format o))) | Set-Content $log
$argsList=$command.Split(' ')
& go @argsList *>> $log
$code=$LASTEXITCODE
('EXIT: '+$code) | Add-Content $log
Write-Output ($i.ToString()+' EXIT '+$code)
}
