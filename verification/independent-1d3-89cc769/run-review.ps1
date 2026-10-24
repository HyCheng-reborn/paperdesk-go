$ErrorActionPreference='Continue'
Set-Location C:/Zhy/code/go-rag-review-1d3/server
$dest='C:/Zhy/code/go-rag/verification/independent-1d3-89cc769'
$env:GOPROXY='off'; $env:GOSUMDB='off'
Copy-Item -LiteralPath ($dest+'/preserved/review_bridge_test.go') -Destination core/review_bridge_test.go
$cmd='test -race ./core -count=1 -v -timeout=60s -run ^TestReviewSyncPanicStopsCancellationBridge$'
@('SHA: 89cc769440b6b405c2ef16eb78a035f486f03fab','LAYER: target + original review reproduction',('CWD: '+(Get-Location)),('CMD: go '+$cmd)) | Set-Content ($dest+'/original-repro.log')
& go @($cmd.Split(' ')) *>> ($dest+'/original-repro.log'); ('EXIT: '+$LASTEXITCODE)|Add-Content ($dest+'/original-repro.log')
Move-Item -LiteralPath core/review_bridge_test.go -Destination ($dest+'/preserved/executed-original-review_bridge_test.go')
$commands=@(
'test ./core ./internal/logic/rag ./internal/logic/knowledge -count=1 -v -timeout=90s -run ^(TestIndex_Sync(Panic|Error)_StopsAfterFuncBridge|TestIndex_BackgroundSaveChunksFailure_NoDuplicateFailed|TestReset_Shutdown(Timeout_PreservesDependencies|Success_CleansUp)|TestSaveChunks_WithoutStatusUpdater_SkipsStatusWrite|TestShutdownCoordinator_.*)$',
'test ./core ./internal/logic/rag/... ./internal/logic/knowledge/... ./internal/cmd/... -count=1 -timeout=120s',
'test -race ./core ./internal/logic/rag/... ./internal/logic/knowledge/... ./internal/cmd/... -count=1 -timeout=120s',
'test -race ./core ./internal/logic/rag ./internal/logic/knowledge -count=10 -v -timeout=180s -run ^(TestIndex_Sync(Panic|Error)_StopsAfterFuncBridge|TestIndex_BackgroundSaveChunksFailure_NoDuplicateFailed|TestReset_Shutdown(Timeout_PreservesDependencies|Success_CleansUp)|TestSaveChunks_WithoutStatusUpdater_SkipsStatusWrite|TestShutdownCoordinator_.*)$',
'test -p 1 ./... -count=1 -timeout=120s',
'build ./...',
'vet ./...',
'test ./core ./internal/logic/rag ./internal/logic/knowledge ./internal/cmd -list .'
)
$i=0
foreach($command in $commands){$i++; $log=$dest+'/target-'+$i+'.log'; @('SHA: 89cc769440b6b405c2ef16eb78a035f486f03fab','LAYER: clean target',('CWD: '+(Get-Location)),('CMD: go '+$command),'GOPROXY=off GOSUMDB=off',('START: '+(Get-Date -Format o)))|Set-Content $log; & go @($command.Split(' ')) *>> $log; $code=$LASTEXITCODE; ('EXIT: '+$code)|Add-Content $log; Write-Output ($i.ToString()+' EXIT '+$code)}
