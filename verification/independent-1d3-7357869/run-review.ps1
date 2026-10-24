$ErrorActionPreference='Continue'
Set-Location C:/Zhy/code/go-rag-review-1d3/server
$dest='C:/Zhy/code/go-rag/verification/independent-1d3-7357869'
$env:GOPROXY='off'; $env:GOSUMDB='off'
Copy-Item ($dest+'/preserved/review_bridge_paths_test.go') core/review_bridge_paths_test.go
Copy-Item ($dest+'/preserved/review_reset_test.go') internal/logic/rag/review_reset_test.go
$cmd='test -race ./core ./internal/logic/rag -count=1 -v -timeout=90s -run ^(TestReviewConcurrentResetUsesStableInstance|TestReviewSyncPanicStopsCancellationBridge|TestReviewBridgeAllSyncExitPaths)$'
@('SHA: 735786966e3e8e8e0f1eb238045ca49209410505','LAYER: target + preserved independent reproductions',('CWD: '+(Get-Location)),('CMD: go '+$cmd))|Set-Content ($dest+'/original-repros.log')
& go @($cmd.Split(' ')) *>> ($dest+'/original-repros.log'); ('EXIT: '+$LASTEXITCODE)|Add-Content ($dest+'/original-repros.log')
Move-Item core/review_bridge_paths_test.go ($dest+'/preserved/executed-review_bridge_paths_test.go')
Move-Item internal/logic/rag/review_reset_test.go ($dest+'/preserved/executed-review_reset_test.go')
$commands=@(
'test ./core ./internal/logic/rag ./internal/logic/knowledge -count=1 -v -timeout=120s -run ^(TestReset_(Concurrent_NoRaceOrPanic|DuringWait_GetRagSvrCallable|ShutdownTimeout_PreservesExactObjects|AfterTaskCompletion_SecondResetSucceeds|ConcurrentInitDoesNotCloseNewInstance)|TestIndex_Background(Chunks.*|CleanupBeforeDone)|TestSaveChunks_(IndexingOnlyUpdater_SkipsFailed|PublicEntry_Compatibility)|TestShutdownCoordinator_.*)$',
'test ./core ./internal/logic/rag/... ./internal/logic/knowledge/... ./internal/cmd/... -count=1 -timeout=150s',
'test -race ./core ./internal/logic/rag/... ./internal/logic/knowledge/... ./internal/cmd/... -count=1 -timeout=150s',
'test -race ./core ./internal/logic/rag ./internal/logic/knowledge -count=10 -v -timeout=180s -run ^(TestReset_(Concurrent_NoRaceOrPanic|DuringWait_GetRagSvrCallable|ShutdownTimeout_PreservesExactObjects|AfterTaskCompletion_SecondResetSucceeds|ConcurrentInitDoesNotCloseNewInstance)|TestIndex_Background(Chunks.*|CleanupBeforeDone)|TestSaveChunks_(IndexingOnlyUpdater_SkipsFailed|PublicEntry_Compatibility)|TestShutdownCoordinator_.*)$',
'test -p 1 ./... -count=1 -timeout=150s',
'build ./...',
'vet ./...',
'test ./core ./internal/logic/rag ./internal/logic/knowledge ./internal/cmd -list .'
)
$i=0
foreach($command in $commands){$i++;$log=$dest+'/target-'+$i+'.log';@('SHA: 735786966e3e8e8e0f1eb238045ca49209410505','LAYER: clean target',('CWD: '+(Get-Location)),('CMD: go '+$command),'GOPROXY=off GOSUMDB=off',('START: '+(Get-Date -Format o)))|Set-Content $log;& go @($command.Split(' ')) *>> $log;$code=$LASTEXITCODE;('EXIT: '+$code)|Add-Content $log;Write-Output ($i.ToString()+' EXIT '+$code)}
